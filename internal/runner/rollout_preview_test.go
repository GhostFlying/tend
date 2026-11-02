package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/harness/rollout"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRolloutPreviewRequiresMutualHelloNegotiation(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, ack := range []bool{false, true} {
			t.Run(map[bool]string{true: "roots", false: "disabled"}[configured]+"/"+map[bool]string{true: "ack", false: "old-server"}[ack], func(t *testing.T) {
				r := controlRunner(t)
				r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 747)
				if configured {
					r.cfg.RolloutRoots = []string{t.TempDir()}
				}
				var advertised bool
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
					var env protocol.Envelope
					_ = json.NewDecoder(q.Body).Decode(&env)
					var hello protocol.HelloPayload
					_ = json.Unmarshal(env.Payload, &hello)
					advertised = slices.Contains(hello.Runtime.Capabilities, protocol.CapabilityRolloutPreview)
					raw, _ := json.Marshal(protocol.HelloResult{RolloutPreview: ack, DeploymentID: "deployment"})
					_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
				}))
				defer server.Close()
				r.c.base, r.c.http = server.URL, server.Client()
				if err := r.freshHello(t.Context()); err != nil {
					t.Fatal(err)
				}
				if advertised != configured || r.previewSupported != (configured && ack) {
					t.Fatal("unnegotiated preview", advertised, r.previewSupported)
				}
			})
		}
	}
}
func nativeFixture(t *testing.T, r *Runner) string {
	t.Helper()
	root := t.TempDir()
	r.cfg.RolloutRoots = []string{root}
	r.previewDeployment = "deployment"
	path := filepath.Join(root, "rollout-session.jsonl")
	body := `{"type":"session_meta","payload":{"id":"session"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"first"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"text":"first run"}]}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"second"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"text":"next manager run"}]}}
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestRolloutPreviewHistoricalManagerTurnAndDeploymentIsolation(t *testing.T) {
	r := controlRunner(t)
	root := nativeFixture(t, r)
	work := protocol.RolloutPreviewWork{DeploymentID: "deployment", RunID: "run", RequireTurn: true, Sources: []protocol.RolloutPreviewSource{{SessionID: "session", TurnID: "first", Path: "/untrusted"}}}
	reader := rollout.New("instance", root)
	got := r.readRollout(t.Context(), reader, work)
	raw, _ := json.Marshal(got)
	if got.State != "ready" || !strings.Contains(string(raw), "first run") || strings.Contains(string(raw), "next manager run") {
		t.Fatal(string(raw))
	}
	// The persistent locator outlives the request and a reader/runner restart.
	work.Sources = nil
	got = r.readRollout(t.Context(), rollout.New("new-instance", root), work)
	if got.State != "ready" {
		t.Fatal(got)
	}
	work.RunID = "next-run"
	work.Sources = []protocol.RolloutPreviewSource{{SessionID: "session", TurnID: "first"}}
	if got := r.readRollout(t.Context(), reader, work); got.State != "source_unknown" {
		t.Fatal("cross-run turn", got)
	}
	work.Sources = []protocol.RolloutPreviewSource{{SessionID: "session", TurnID: "second"}}
	if got := r.readRollout(t.Context(), reader, work); got.State != "ready" {
		t.Fatal("manager reuse", got)
	}
	work.RunID = "foreign"
	work.Sources = []protocol.RolloutPreviewSource{{SessionID: "session"}}
	if got := r.readRollout(t.Context(), reader, work); got.State != "source_unknown" {
		t.Fatal("manager lacks turn", got)
	}
}
func TestRolloutPreviewLoopUsesOnlyDiagnosticTransportWhilePaused(t *testing.T) {
	r := controlRunner(t)
	nativeFixture(t, r)
	r.previewSupported = true
	r.control.Mode = protocol.RunnerMaintenance
	r.controlLoaded = true
	r.c.token, r.c.hostID = "host-token", "host"
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	jobs := make(chan protocol.RolloutPreviewWork, 1)
	jobs <- protocol.RolloutPreviewWork{RequestID: "read", DeploymentID: "deployment", InstanceID: r.instanceID, RunID: "run", DeadlineMillis: time.Now().Add(time.Second * 2).UnixMilli(), Sources: []protocol.RolloutPreviewSource{{SessionID: "session", TurnID: "first"}}}
	replies := make(chan protocol.RolloutPreviewReply, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path != "/runner/v1/rollout-preview" || q.Header.Get("Authorization") != "Bearer host-token" || q.Header.Get(headerHost) != "host" {
			t.Error("unexpected execution request", q.URL)
			w.WriteHeader(403)
			return
		}
		if q.Method == http.MethodGet {
			select {
			case work := <-jobs:
				_ = json.NewEncoder(w).Encode(work)
			case <-q.Context().Done():
			}
			return
		}
		var reply protocol.RolloutPreviewReply
		if err := json.NewDecoder(q.Body).Decode(&reply); err != nil {
			t.Error(err)
		}
		replies <- reply
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	done := make(chan struct{})
	go func() { defer close(done); r.rolloutPreviews(ctx) }()
	select {
	case reply := <-replies:
		if reply.Result.State != "ready" || reply.RequestID != "read" {
			t.Fatal(reply)
		}
	case <-ctx.Done():
		t.Fatal("paused diagnostics stalled")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("diagnostics leaked")
	}
}

func TestRolloutPreviewIndexesNativeLifecycleAndConfirmedChildTurns(t *testing.T) {
	r := controlRunner(t)
	nativeFixture(t, r)
	for _, event := range []struct{ kind, session, turn string }{{"native.turn_started", "session", "first"}, {"native.child", "child-session", "child-turn"}, {"native.turn_completed", "session", "first"}} {
		body, _ := json.Marshal(map[string]string{"thread_id": event.session, "turn_id": event.turn})
		if err := r.recordRolloutEvent(t.Context(), "run", harness.WorkerEvent{Kind: event.kind, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := r.cfg.Journal.RolloutSources(t.Context(), "deployment", "run")
	if err != nil || len(sources) != 2 || sources[0].TurnID != "first" || sources[1].SessionID != "child-session" {
		t.Fatal(sources, err)
	}
	work := protocol.RolloutPreviewWork{DeploymentID: "another-deployment", RunID: "run", Sources: sources}
	if got := r.readRollout(t.Context(), rollout.New("instance", r.cfg.RolloutRoots...), work); got.State != "source_unknown" {
		t.Fatal(got)
	}
}

func TestRolloutPreviewOrcaLocatorProtectsCleanupBeforeAnyOwnerRead(t *testing.T) {
	r := controlRunner(t)
	root := nativeFixture(t, r)
	body, _ := json.Marshal(protocol.NativeIdentity{Provider: "orca", State: "known", SessionID: "session", TurnID: "first"})
	if err := r.recordRolloutEvent(t.Context(), "run", harness.WorkerEvent{Kind: protocol.EventNativeIdentity, Body: body}); err != nil {
		t.Fatal(err)
	}
	sources, err := r.cfg.Journal.RolloutSources(t.Context(), "deployment", "run")
	if err != nil || len(sources) != 1 || sources[0].Path != filepath.Join(root, "rollout-session.jsonl") {
		t.Fatal(sources, err)
	}
	if err := r.cfg.Journal.ProtectRollouts(t.Context(), root); err == nil {
		t.Fatal("native originals were deletable before first preview")
	}
}

func TestRolloutPreviewQueuesDeliveredWorkAndCancelsWaitingReads(t *testing.T) {
	for _, cancelWaiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelWaiting), func(t *testing.T) {
			r := controlRunner(t)
			nativeFixture(t, r)
			r.previewSupported = true
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			jobs := make(chan protocol.RolloutPreviewWork, 6)
			posted := make(chan protocol.RolloutPreviewReply, 8)
			polls := make(chan struct{}, 16)
			release := make(chan struct{})
			for i := range 4 {
				jobs <- protocol.RolloutPreviewWork{RequestID: fmt.Sprintf("read-%d", i), DeploymentID: "deployment", InstanceID: r.instanceID, RunID: "run", DeadlineMillis: time.Now().Add(4 * time.Second).UnixMilli(), Sources: []protocol.RolloutPreviewSource{{SessionID: "session", TurnID: "first"}}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.Method == http.MethodGet {
					select {
					case polls <- struct{}{}:
					case <-q.Context().Done():
						return
					}
					select {
					case work := <-jobs:
						_ = json.NewEncoder(w).Encode(work)
					case <-q.Context().Done():
					}
					return
				}
				var reply protocol.RolloutPreviewReply
				if err := json.NewDecoder(q.Body).Decode(&reply); err != nil {
					t.Error(err)
					return
				}
				select {
				case posted <- reply:
				case <-q.Context().Done():
					return
				}
				if reply.RequestID != "read-4" {
					select {
					case <-release:
					case <-q.Context().Done():
						return
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			r.c.base, r.c.http = server.URL, server.Client()
			done := make(chan struct{})
			go func() { defer close(done); r.rolloutPreviews(ctx) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("diagnostic queue did not stop")
				}
				server.Close()
			}()
			// All four slots remain occupied by reply uploads before delivering a fifth job.
			for range 4 {
				select {
				case reply := <-posted:
					if reply.Result.State != "ready" {
						t.Fatal(reply)
					}
				case <-ctx.Done():
					t.Fatal("initial reads did not fill the slots")
				}
			}
			jobs <- protocol.RolloutPreviewWork{RequestID: "read-4", DeploymentID: "deployment", InstanceID: r.instanceID, RunID: "run", DeadlineMillis: time.Now().Add(4 * time.Second).UnixMilli(), Sources: []protocol.RolloutPreviewSource{{SessionID: "session", TurnID: "first"}}}
			expectedPolls := 6
			if cancelWaiting {
				jobs <- protocol.RolloutPreviewWork{RequestID: "read-4", Cancelled: true}
				expectedPolls++
			}
			// The next long poll proves the delivered fifth job (and optional cancellation) was processed.
			for range expectedPolls {
				select {
				case <-polls:
				case <-ctx.Done():
					t.Fatal("diagnostic poll stopped while reads were occupied")
				}
			}
			close(release)
			if cancelWaiting {
				select {
				case reply := <-posted:
					t.Fatal("cancelled queued read replied", reply)
				case <-time.After(50 * time.Millisecond):
				}
			} else {
				select {
				case reply := <-posted:
					if reply.RequestID != "read-4" || reply.Result.State != "ready" {
						t.Fatal(reply)
					}
				case <-ctx.Done():
					t.Fatal("delivered fifth read was dropped instead of queued")
				}
			}
		})
	}
}

func TestRolloutPreviewDoesNotAdvanceNativeUsageState(t *testing.T) {
	r := controlRunner(t)
	root := nativeFixture(t, r)
	source := journal.UsageSource{ID: "usage", RunID: "run", RegistrationQueued: true, Coverage: "registered", Source: nativeusage.Source{Provider: "traex", SessionID: "session", ThreadID: "session", Path: filepath.Join(root, "rollout-session.jsonl")}, Cursors: map[string]nativeusage.Cursor{"original": {Offset: 42, Prefix: "retained"}}}
	if err := r.cfg.Journal.RegisterUsageSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	before, err := r.cfg.Journal.UsageSources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	requests, err := r.cfg.Journal.Requests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	work := protocol.RolloutPreviewWork{DeploymentID: "deployment", RunID: "run", Sources: []protocol.RolloutPreviewSource{{SessionID: "session", TurnID: "first"}}}
	for range 2 {
		if got := r.readRollout(t.Context(), rollout.New("preview", root), work); got.State != "ready" {
			t.Fatal(got)
		}
	}
	after, err := r.cfg.Journal.UsageSources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nextRequests, err := r.cfg.Journal.Requests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(before)
	current, _ := json.Marshal(after)
	if string(original) != string(current) || len(nextRequests) != len(requests) {
		t.Fatalf("preview changed usage state or queued an upload: %s -> %s", original, current)
	}
}
