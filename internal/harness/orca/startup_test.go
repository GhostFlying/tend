package orca

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func startupFixture(t *testing.T) (*workerFixture, *worker) {
	t.Helper()
	f := newWorkerFixture(t)
	f.orca.noState = true
	f.orca.launchPromptOutcome = promptNotDelivered
	f.orca.launchDiagnostic = &LaunchDiagnostic{Code: "readiness_timeout", Phase: "readiness", PromptWriteState: "none", TerminalHandle: "term_1", ElapsedMS: 60000}
	w := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(protocol.RoleImplement)}
	return f, w
}

func TestStartupRetryOnlyExplicitNoWriteTimeout(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*workerFixture)
		launches int
	}{
		{"timeout", func(*workerFixture) {}, 2},
		{"legacy", func(f *workerFixture) { f.orca.launchDiagnostic = nil }, 1},
		{"possible write", func(f *workerFixture) { f.orca.launchDiagnostic.PromptWriteState = "possible" }, 1},
		{"unknown outcome", func(f *workerFixture) { f.orca.launchPromptOutcome = promptUnconfirmed }, 1},
		{"interactive", func(f *workerFixture) { f.orca.launchDiagnostic.Code = "interactive_prompt" }, 1},
		{"wrong terminal", func(f *workerFixture) { f.orca.launchDiagnostic.TerminalHandle = "other" }, 1},
		{"disabled", func(f *workerFixture) { n := 0; f.ws.opts.StartupAttempts = &n }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := startupFixture(t)
			tc.mutate(f)
			if err := w.beginStartup(t.Context(), "trae"); err == nil {
				t.Fatal("expected failed launch")
			}
			if got := f.orca.count("agent launch", nil); got != tc.launches {
				t.Fatalf("launches=%d", got)
			}
			// Restart cannot replenish the startup budget or replay a failed receipt.
			var restored workerState
			if err := json.Unmarshal([]byte(f.thread), &restored); err != nil {
				t.Fatal(err)
			}
			w.st = restored
			if err := w.beginStartup(t.Context(), "trae"); err == nil {
				t.Fatal("expected persisted failure")
			}
			if got := f.orca.count("agent launch", nil); got != tc.launches {
				t.Fatalf("restart launches=%d", got)
			}
		})
	}
}

func TestStartupRetryCloseFailureAndOwnerActivity(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		f, w := startupFixture(t)
		f.orca.onLaunch = func() error {
			if takeover {
				f.orca.agents["tab:1"] = &Agent{PaneKey: "tab:1", Prompt: "owner's work", State: stateWorking}
			} else {
				f.orca.failClose = true
			}
			return nil
		}
		err := w.beginStartup(t.Context(), "trae")
		if err == nil {
			t.Fatal("expected reconciliation")
		}
		if takeover && !errors.Is(err, harness.ErrHeld) {
			t.Fatalf("err=%v", err)
		}
		if f.orca.count("agent launch", nil) != 1 {
			t.Fatal("unsafe retry")
		}
		if w.st.Startup == nil || w.st.Startup.Phase != "closing" || w.st.Startup.Attempts != 1 {
			t.Fatalf("state=%+v", w.st)
		}
	}
}

func TestStartupRetrySurvivesCrashBeforeReplacement(t *testing.T) {
	f, w := startupFixture(t)
	if err := w.launch(t.Context(), "trae"); err == nil {
		t.Fatal("expected timeout")
	}
	w.st.Startup = &startupState{Attempts: 1, Phase: "closing", Terminal: w.st.Terminal, Diagnostic: w.st.LaunchDiagnostic}
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	// Simulate successful acknowledged close followed by restart before replacement.
	if err := f.ws.releaseUndelivered(t.Context(), w.spec.RunID, w.st, false, false); err != nil {
		t.Fatal(err)
	}
	w.st.Startup.CloseIssued = true
	w.st.Startup.Stopped = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	f.orca.launchPromptOutcome = promptHandedToTerminal
	f.orca.launchDiagnostic = nil
	if err := json.Unmarshal([]byte(f.thread), &w.st); err != nil {
		t.Fatal(err)
	}
	if err := w.beginStartup(t.Context(), "trae"); err != nil {
		t.Fatal(err)
	}
	if w.st.Startup.Attempts != 1 || w.st.Startup.Phase != "started" {
		t.Fatalf("startup=%+v", w.st.Startup)
	}
	if f.orca.count("agent launch", nil) != 2 || f.orca.count("terminal close", nil) != 1 {
		t.Fatal("duplicated provider action")
	}
}

func TestStartupUnknownCloseNeverRestartsOrFinalizes(t *testing.T) {
	f, w := startupFixture(t)
	f.orca.failClose = true
	if err := w.beginStartup(t.Context(), "trae"); err == nil {
		t.Fatal("expected close error")
	}
	f.orca.failClose = false
	// A lost response is not permission to replay the close or launch again.
	if err := w.beginStartup(t.Context(), "trae"); err == nil {
		t.Fatal("expected unknown outcome")
	}
	if f.orca.count("terminal close", nil) != 1 || f.orca.count("agent launch", nil) != 1 {
		t.Fatal("replayed unknown operation")
	}
	if err := f.ws.ReleaseFinalizing(t.Context(), w.spec.RunID, f.thread, now(), false, func(string) error { return nil }); err == nil {
		t.Fatal("finalized unknown close")
	}
}

func TestStartupMissingTerminalWithoutKillProofDoesNotRetry(t *testing.T) {
	f, w := startupFixture(t)
	if err := w.launch(t.Context(), "trae"); err == nil {
		t.Fatal("expected startup timeout")
	}
	// Disappearance alone can represent a detached but still running process.
	f.orca.terms = nil
	if err := w.beginStartup(t.Context(), "trae"); err == nil {
		t.Fatal("expected missing stopped proof")
	}
	if f.orca.count("agent launch", nil) != 1 {
		t.Fatal("started replacement without proof")
	}
}

func TestStartupDiagnosticEventSurvivesFailedStart(t *testing.T) {
	f, w := startupFixture(t)
	zero := 0
	f.ws.opts.StartupAttempts = &zero
	var events []harness.WorkerEvent
	w.spec.OnStartupEvent = func(_ context.Context, e harness.WorkerEvent) error { events = append(events, e); return nil }
	if err := w.beginStartup(t.Context(), "trae"); err == nil {
		t.Fatal("expected startup failure")
	}
	if len(events) != 1 || events[0].Kind != "worker.startup_failed" {
		t.Fatalf("events=%+v", events)
	}
	var body struct {
		Diagnostic LaunchDiagnostic `json:"diagnostic"`
	}
	if err := json.Unmarshal(events[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Diagnostic.Code != "readiness_timeout" || body.Diagnostic.ElapsedMS != 60000 {
		t.Fatalf("diagnostic=%+v", body.Diagnostic)
	}
}

func TestStartupClosingCheckpointWithoutIssuedCloseAllowsFinalization(t *testing.T) {
	f, w := startupFixture(t)
	if err := w.launch(t.Context(), "trae"); err == nil {
		t.Fatal("expected launch timeout")
	}
	// A cancellation/probe error can interrupt recovery after its checkpoint but
	// before any close request. This is not an unknown external side effect.
	w.st.Startup = &startupState{Attempts: 1, Phase: "closing", Terminal: w.st.Terminal, Diagnostic: w.st.LaunchDiagnostic}
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if err := f.ws.ReleaseFinalizing(t.Context(), w.spec.RunID, f.thread, now(), false, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if f.orca.count("terminal close", nil) != 1 || len(f.orca.terms) != 0 {
		t.Fatal("unissued close checkpoint blocked owned cleanup")
	}
	if f.orca.count("agent launch", nil) != 1 {
		t.Fatal("finalization relaunched a worker")
	}
}

func TestStartupSavedSuccessReplaysEventWithoutRelaunch(t *testing.T) {
	f, w := startupFixture(t)
	f.orca.onLaunch = func() error {
		if f.orca.next == 2 {
			f.orca.launchPromptOutcome = promptHandedToTerminal
			f.orca.launchDiagnostic = nil
		}
		return nil
	}
	var attempted harness.WorkerEvent
	w.spec.OnStartupEvent = func(_ context.Context, e harness.WorkerEvent) error {
		if e.Kind == "worker.startup_started" {
			attempted = e
			return errors.New("crash before event append")
		}
		return nil
	}
	if err := w.beginStartup(t.Context(), "trae"); err == nil {
		t.Fatal("expected interrupted event append")
	}
	var saved workerState
	if err := json.Unmarshal([]byte(f.thread), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Startup == nil || saved.Startup.Phase != "started" {
		t.Fatal("success phase not persisted")
	}
	restored := &worker{ws: f.ws, wt: f.orca.wt, spec: f.spec(protocol.RoleImplement), st: saved}
	var replayed []harness.WorkerEvent
	restored.spec.OnStartupEvent = func(_ context.Context, e harness.WorkerEvent) error { replayed = append(replayed, e); return nil }
	if err := restored.beginStartup(t.Context(), "trae"); err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 || replayed[0].Kind != "worker.startup_started" || replayed[0].NativeID != attempted.NativeID || string(replayed[0].Body) != string(attempted.Body) {
		t.Fatalf("unstable replay: attempted=%+v replayed=%+v", attempted, replayed)
	}
	if f.orca.count("agent launch", nil) != 2 || f.orca.count("terminal close", nil) != 1 || f.orca.count("terminal send", nil) != 0 {
		t.Fatal("replaying success mutated native execution")
	}
}

func TestStartupCloseUsesActualCLIResponseEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		want           bool
	}{
		{"confirmed", `{"id":"close-1","ok":true,"result":{"close":{"handle":"term_1","tabId":"tab","ptyKilled":true}},"_meta":{"runtimeId":"runtime-1"}}`, true},
		{"not killed", `{"id":"close-1","ok":true,"result":{"close":{"handle":"term_1","ptyKilled":false}}}`, false},
		{"wrong terminal", `{"id":"close-1","ok":true,"result":{"close":{"handle":"other","ptyKilled":true}}}`, false},
		{"missing nested receipt", `{"id":"close-1","ok":true,"result":{"ptyKilled":true}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w := startupFixture(t)
			original := f.ws.opts.Client.Exec
			f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
				reply, err := original(ctx, dir, args)
				if err == nil && args[0] == "terminal" && args[1] == "close" {
					return []byte(tc.response), nil
				}
				return reply, err
			}
			f.orca.onLaunch = func() error {
				if f.orca.next == 2 {
					f.orca.launchPromptOutcome = promptHandedToTerminal
					f.orca.launchDiagnostic = nil
				}
				return nil
			}
			err := w.beginStartup(t.Context(), "trae")
			if (err == nil) != tc.want {
				t.Fatalf("err=%v wantSuccess=%v", err, tc.want)
			}
			launches := 1
			if tc.want {
				launches = 2
			}
			if f.orca.count("agent launch", nil) != launches || f.orca.count("terminal close", nil) != 1 {
				t.Fatal("incorrect close proof permitted or blocked replacement")
			}
		})
	}
}
