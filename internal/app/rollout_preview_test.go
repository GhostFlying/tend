package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func previewFixture(t *testing.T) (*harness, protocol.Assignment) {
	t.Helper()
	h := newHarness(t)
	r := testRuntime("preview-instance")
	r.Capabilities = append(r.Capabilities, protocol.CapabilityRolloutPreview)
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.createTask(owner, createTaskPayload{Title: "preview", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no assignment")
	}
	if _, err := h.app.DeploymentID(t.Context()); err != nil {
		t.Fatal(err)
	}
	return h, as
}

func TestRolloutPreviewIsAnEphemeralReadOutsideMaintenanceAndLeases(t *testing.T) {
	h, as := previewFixture(t)
	// Even an occupied runner in maintenance serves reads without a new claim.
	op := h.exec(owner, h.env("hosts.maintenance", protocol.Target{Type: "host", ID: "linux-1"}, map[string]any{"mode": protocol.RunnerMaintenance}, rev(0)), HostMaintenance{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	counts := map[string]int{}
	for _, table := range []string{"request_records", "events", "outbox", "runs", "run_logs", "token_usage_facts", "token_usage_current", "token_usage_bindings", "token_usage_sources", "token_usage_local_sources", "token_usage_binding_revisions"} {
		counts[table] = h.count(table)
	}
	prior := h.run(as.RunID)
	got := make(chan protocol.RolloutPreview, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go func() {
		result, err := h.app.ReadRolloutPreview(ctx, owner, as.RunID, protocol.RolloutPreviewQuery{})
		if err != nil {
			t.Error(err)
		}
		got <- result
	}()
	work, err := h.app.PollRolloutPreview(ctx, runner("linux-1"), "preview-instance")
	if err != nil || work == nil || work.RunID != as.RunID || work.DeploymentID == "" {
		t.Fatal(work, err)
	}
	reply := protocol.RolloutPreviewReply{RequestID: work.RequestID, InstanceID: work.InstanceID, Result: protocol.RolloutPreview{State: "ready", Generation: "native", Page: &protocol.RunRolloutPage{RunID: "untrusted", Observed: protocol.RunFailed, Logs: []protocol.RunLogView{}}}}
	if err := h.app.ReplyRolloutPreview(ctx, runner("another-host"), reply); err == nil {
		t.Fatal("foreign runner accepted")
	}
	if err := h.app.ReplyRolloutPreview(ctx, runner("linux-1"), reply); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-got:
		if result.State != "ready" || result.Page.RunID != as.RunID || result.Page.Observed != prior.ObservedState {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal("read did not finish")
	}
	for table, count := range counts {
		if h.count(table) != count {
			t.Fatalf("read mutated %s", table)
		}
	}
	if err := h.app.ReplyRolloutPreview(ctx, runner("linux-1"), reply); err != nil {
		t.Fatal("late reply", err)
	}
	if len(h.app.preview.pending) != 0 {
		t.Fatal("body retained after read")
	}
}

func TestRolloutPreviewCancellationAndRestartNeverReplayBusinessWork(t *testing.T) {
	h, as := previewFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.app.ReadRolloutPreview(ctx, owner, as.RunID, protocol.RolloutPreviewQuery{})
	}()
	pollCtx, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	work, err := h.app.PollRolloutPreview(pollCtx, runner("linux-1"), "preview-instance")
	if err != nil || work == nil {
		t.Fatal(work, err)
	}
	cancel()
	<-done
	cancelled, err := h.app.PollRolloutPreview(pollCtx, runner("linux-1"), "preview-instance")
	if err != nil || cancelled == nil || !cancelled.Cancelled || cancelled.RequestID != work.RequestID {
		t.Fatal(cancelled, err)
	}
	restarted := New(h.store, h.ids, h.clock.Now)
	if err := restarted.ReplyRolloutPreview(pollCtx, runner("linux-1"), protocol.RolloutPreviewReply{InstanceID: "preview-instance", RequestID: work.RequestID, Result: protocol.RolloutPreview{State: "ready"}}); err != nil {
		t.Fatal(err)
	}
	if len(restarted.preview.pending) != 0 {
		t.Fatal("server restart restored body")
	}
	next := testRuntime("replacement")
	next.Capabilities = append(next.Capabilities, protocol.CapabilityRolloutPreview)
	if op := h.versionedHello("linux-1", next); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.PollRolloutPreview(pollCtx, runner("linux-1"), "preview-instance"); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("stale instance", err)
	}
}

func TestRolloutPreviewRejectsOtherPrincipalsAndOldRunners(t *testing.T) {
	h, as := previewFixture(t)
	if _, err := h.app.ReadRolloutPreview(t.Context(), runner("linux-1"), as.RunID, protocol.RolloutPreviewQuery{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
	old := testRuntime("old-runner")
	if op := h.versionedHello("linux-1", old); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	result, err := h.app.ReadRolloutPreview(t.Context(), owner, as.RunID, protocol.RolloutPreviewQuery{})
	if err != nil || result.State != "unsupported" {
		t.Fatal(result, err)
	}
	if _, err := h.app.PollRolloutPreview(t.Context(), runner("linux-1"), "old-runner"); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
}

func TestRolloutPreviewHistoricalManagerUsesCompletedReplyEnvelope(t *testing.T) {
	h, as := previewFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE runs SET role='issue_manager' WHERE run_id=?`, as.RunID); err != nil {
			return err
		}
		for _, candidate := range []struct {
			id, status string
			reply      protocol.ManagerNativeReply
		}{
			{"valid", "completed", protocol.ManagerNativeReply{ID: "valid", Result: json.RawMessage(`"turn"`)}},
			{"failed", "completed", protocol.ManagerNativeReply{ID: "failed", Result: json.RawMessage(`"foreign"`), Error: "failed"}},
			{"rejected", "completed", protocol.ManagerNativeReply{ID: "rejected", Result: json.RawMessage(`"foreign"`), Rejected: true}},
			{"pending", "pending", protocol.ManagerNativeReply{ID: "pending", Result: json.RawMessage(`"foreign"`)}},
		} {
			raw, _ := json.Marshal(candidate.reply)
			if _, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,result_json,status,created_at) VALUES(?,?,'turn',?,?,?,?)`, candidate.id, as.RunID, `{"thread_id":"manager-session"}`, string(raw), candidate.status, h.clock.Now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := h.app.ReadRolloutPreview(ctx, owner, as.RunID, protocol.RolloutPreviewQuery{})
		done <- err
	}()
	work, err := h.app.PollRolloutPreview(ctx, runner("linux-1"), "preview-instance")
	if err != nil || work == nil {
		t.Fatal(work, err)
	}
	if !work.RequireTurn || len(work.Sources) != 1 || work.Sources[0].SessionID != "manager-session" || work.Sources[0].TurnID != "turn" {
		t.Fatal(work)
	}
	if err := h.app.ReplyRolloutPreview(ctx, runner("linux-1"), protocol.RolloutPreviewReply{RequestID: work.RequestID, InstanceID: work.InstanceID, Result: protocol.RolloutPreview{State: "empty"}}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
