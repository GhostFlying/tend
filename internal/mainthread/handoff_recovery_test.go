package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func assertCurrentRecoveredThread(t *testing.T, f *handoffFixture, target harness.Thread) {
	t.Helper()
	hash, err := currentToolsetHash()
	if err != nil {
		t.Fatal(err)
	}
	binding, exists, err := f.e.app.MainThreadBinding(context.Background(), "main")
	if err != nil || !exists || binding.ThreadID != target.ID() || binding.ToolsetHash != hash {
		t.Fatalf("recovered binding %+v err=%v", binding, err)
	}
	created := target.(*handoffTestThread).Thread.(*testkit.FakeThread)
	found := false
	for _, spec := range created.Config.Tools {
		if spec.Name == "files_read" {
			found = true
		}
	}
	if !found || created.Config.OnToolCall == nil {
		t.Fatal("recovered native thread lacks current tool definitions/handler")
	}
	if len(businessStarts(f.e.h)) != 0 {
		t.Fatal("startup replayed an owner turn during recovery")
	}
}

func TestSchemaStartupWaitsForUnknownNativeReceiptWithoutReplay(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := context.Background()
	message := f.e.say("Owner request whose turn/start result was lost")
	var requestID string
	if err := f.e.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT request_id FROM outbox WHERE json_extract(payload_json,'$.message_id')=? AND operation='main_thread.handle_message'`, message).Scan(&requestID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE outbox SET status='unknown',attempts=1 WHERE request_id=?`, requestID); err != nil {
			return err
		}
		return tx.PutMainTurn(ctx, store.MainTurn{RequestID: requestID, MessageID: message, ThreadID: f.source.ID(), Status: store.TurnStarting}, f.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	target, err := f.e.m.openManagedThread(ctx, f.session)
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome || target != nil {
		t.Fatalf("unreconciled source migrated: %v %v", target, err)
	}
	recorded, exists, err := f.e.app.MainTurn(ctx, requestID)
	if err != nil || !exists || recorded.Status != store.TurnStarting || recorded.ThreadID != f.source.ID() || recorded.TurnID != "" {
		t.Fatalf("uncertain old turn rewritten: %+v %v", recorded, err)
	}
	if f.e.count(`SELECT COUNT(*) FROM outbox WHERE request_id=? AND status='unknown' AND attempts=1`, requestID) != 1 {
		t.Fatal("unknown delivery reset or replayed")
	}
	binding, _, err := f.e.app.MainThreadBinding(ctx, "main")
	if err != nil || binding.ThreadID != f.source.ID() || len(businessStarts(f.e.h)) != 0 {
		t.Fatalf("source replaced or execution replayed: %+v %v", binding, err)
	}
}

func seedPendingRecoveryHandoff(t *testing.T, f *handoffFixture, phase string) (store.ThreadHandoff, harness.Thread) {
	t.Helper()
	ctx := context.Background()
	snap, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.e.app.PrepareHandoff(ctx, store.ThreadHandoff{ID: "older-schema-handoff", Instance: "main", SourceThreadID: f.source.ID(), SourceGeneration: snap.BindingGeneration, SourceVersion: snap.Version, ToolsetHash: "intermediate-obsolete-toolset", Prompt: "Older confirmed handoff reference package"})
	if err != nil {
		t.Fatal(err)
	}
	if phase == "prepared" {
		return job, nil
	}
	if err := f.e.app.AdvanceHandoff(ctx, job.ID, "prepared", "creating"); err != nil {
		t.Fatal(err)
	}
	if phase == "creating" {
		return job, nil
	}
	if phase == "unknown" {
		if err := f.e.app.AdvanceHandoff(ctx, job.ID, "creating", "unknown"); err != nil {
			t.Fatal(err)
		}
		return job, nil
	}
	target, err := f.session.StartThread(ctx, harness.ThreadConfig{Tools: []harness.ToolSpec{{Name: "old_schema_tool", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.RecordHandoffTarget(ctx, job.ID, target.ID(), rolloutPath(target)); err != nil {
		t.Fatal(err)
	}
	if phase == "created" {
		return job, target
	}
	if err := f.e.app.AdvanceHandoff(ctx, job.ID, "created", "injecting"); err != nil {
		t.Fatal(err)
	}
	if phase == "injecting" {
		return job, target
	}
	if err := target.InjectDeveloper(ctx, job.Prompt); err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.AdvanceHandoff(ctx, job.ID, "injecting", "injected"); err != nil {
		t.Fatal(err)
	}
	return job, target
}

func TestStartupRetargetsOnlyUnsentPreparedHandoff(t *testing.T) {
	f := newHandoffFixture(t)
	f.saveSummary(t)
	job, _ := seedPendingRecoveryHandoff(t, f, "prepared")
	target, err := f.e.m.openManagedThread(context.Background(), f.session)
	if err != nil {
		t.Fatal(err)
	}
	assertCurrentRecoveredThread(t, f, target)
	saved, exists, err := f.e.app.GetHandoff(context.Background(), job.ID)
	hash, _ := currentToolsetHash()
	if err != nil || !exists || saved.Phase != "committed" || saved.ToolsetHash != hash || saved.TargetThreadID != target.ID() {
		t.Fatalf("prepared handoff not retargeted %+v %v", saved, err)
	}
	starts, _, _ := f.session.counts()
	if starts != 2 {
		t.Fatalf("unsent preparation created an obsolete intermediate target: starts=%d", starts)
	}
}

func TestStartupCompletesConfirmedObsoleteTargetBeforeCurrentSchemaReadiness(t *testing.T) {
	for _, phase := range []string{"created", "injected"} {
		t.Run(phase, func(t *testing.T) {
			f := newHandoffFixture(t)
			f.saveSummary(t)
			job, intermediate := seedPendingRecoveryHandoff(t, f, phase)
			checked := false
			f.session.resumeHook = func(ctx context.Context, id string, cfg harness.ThreadConfig) {
				if id != intermediate.ID() {
					return
				}
				checked = true
				f.e.m.mu.Lock()
				quarantined := f.e.m.maintenanceBlocked
				f.e.m.mu.Unlock()
				if !quarantined {
					t.Error("intermediate target resumed outside maintenance quarantine")
				}
				result := cfg.OnToolCall(ctx, harness.ToolCall{ThreadID: id, TurnID: "late-old-turn", CallID: "obsolete-callback", Tool: "decisions_open", Arguments: json.RawMessage(`not-valid-json`)})
				if result.Success || !strings.Contains(result.Text, "disabled") {
					t.Error("obsolete intermediate callback reached current tool parser")
				}
			}
			target, err := f.e.m.openManagedThread(context.Background(), f.session)
			if err != nil {
				t.Fatal(err)
			}
			assertCurrentRecoveredThread(t, f, target)
			if !checked || target.ID() == intermediate.ID() {
				t.Fatal("obsolete confirmed target became ready without another current-schema handoff")
			}
			saved, exists, err := f.e.app.GetHandoff(context.Background(), job.ID)
			if err != nil || !exists || saved.Phase != "committed" || saved.ToolsetHash != job.ToolsetHash || saved.TargetThreadID != intermediate.ID() {
				t.Fatalf("confirmed old intent rewritten %+v %v", saved, err)
			}
			binding, _, _ := f.e.app.MainThreadBinding(context.Background(), "main")
			if len(binding.Ancestors) != 2 || binding.Ancestors[0].ThreadID != f.source.ID() || binding.Ancestors[1].ThreadID != intermediate.ID() {
				t.Fatalf("lost intermediate ancestry %+v", binding.Ancestors)
			}
			starts, _, _ := f.session.counts()
			if starts != 3 {
				t.Fatalf("native thread starts=%d want source+confirmed obsolete+current", starts)
			}
		})
	}
}

func TestStartupDoesNotSupersedeUncertainObsoleteHandoff(t *testing.T) {
	for _, phase := range []string{"creating", "injecting", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			f := newHandoffFixture(t)
			job, _ := seedPendingRecoveryHandoff(t, f, phase)
			before, _, _ := f.session.counts()
			if _, err := f.e.m.openManagedThread(context.Background(), f.session); err == nil {
				t.Fatal("uncertain obsolete native operation was superseded")
			}
			after, _, _ := f.session.counts()
			if before != after {
				t.Fatal("uncertain obsolete operation spawned a replacement")
			}
			binding, _, _ := f.e.app.MainThreadBinding(context.Background(), "main")
			if binding.ThreadID != f.source.ID() {
				t.Fatal("uncertain operation changed source authority")
			}
			saved, exists, err := f.e.app.GetHandoff(context.Background(), job.ID)
			if err != nil || !exists || saved.Phase != phase || saved.ToolsetHash != job.ToolsetHash {
				t.Fatalf("uncertain intent rewritten %+v %v", saved, err)
			}
		})
	}
}

func TestStartupMissingSourceUsesSummaryAndHistoryReference(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "facts-only"
		if cached {
			name = "cached-summary"
		}
		t.Run(name, func(t *testing.T) {
			f := newHandoffFixture(t)
			ctx := context.Background()
			f.e.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}}}, app.SyncConfig{})
			f.e.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "SOURCE_UNAVAILABLE_LEDGER_TASK", "spec": spec}, app.CreateTask{})

			if cached {
				f.saveSummary(t)
			}
			f.session.resumeErr = map[string]error{f.source.ID(): harness.ErrRejected}
			target, err := f.e.m.openManagedThread(ctx, f.session)
			if err != nil {
				t.Fatal(err)
			}
			assertCurrentRecoveredThread(t, f, target)
			injected := strings.Join(f.e.h.Injected(target.ID()), "\n")
			for _, marker := range []string{f.source.ID(), "rollout:" + f.source.ID(), "SOURCE_UNAVAILABLE_LEDGER_TASK", `"source_native_available":false`} {
				if !strings.Contains(injected, marker) {
					t.Errorf("fallback missing %q", marker)
				}
			}
			if cached && !strings.Contains(injected, "CACHED_SUMMARY_MARKER") {
				t.Fatal("missing-source fallback lost cached summary")
			}
			starts, forks, _ := f.session.counts()
			if starts != 2 || forks != 0 {
				t.Fatalf("missing source attempted native summary: starts=%d forks=%d", starts, forks)
			}
		})
	}
}

func TestStartupUncertainSourceResumeNeverCreatesReplacement(t *testing.T) {
	f := newHandoffFixture(t)
	f.saveSummary(t)
	f.session.resumeErr = map[string]error{f.source.ID(): errors.New("native resume response lost")}
	if _, err := f.e.m.openManagedThread(context.Background(), f.session); err == nil {
		t.Fatal("uncertain source resume treated as missing source")
	}
	starts, forks, _ := f.session.counts()
	if starts != 1 || forks != 0 {
		t.Fatal("uncertain source resume spawned replacement or summary")
	}
	binding, _, _ := f.e.app.MainThreadBinding(context.Background(), "main")
	if binding.ThreadID != f.source.ID() {
		t.Fatal("uncertain source resume changed authority")
	}
}
