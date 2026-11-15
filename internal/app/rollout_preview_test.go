package app

import (
	"context"
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
	for _, table := range []string{"conversation_messages", "events", "outbox", "runs", "run_logs", "token_usage_facts", "token_usage_current", "token_usage_bindings", "token_usage_sources", "token_usage_local_sources", "token_usage_binding_revisions"} {
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
	work, err := h.app.PollRolloutPreview(ctx, runner("linux-1"), "preview-instance", nil)
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
	work, err := h.app.PollRolloutPreview(pollCtx, runner("linux-1"), "preview-instance", nil)
	if err != nil || work == nil {
		t.Fatal(work, err)
	}
	cancel()
	<-done
	cancelled, err := h.app.PollRolloutPreview(pollCtx, runner("linux-1"), "preview-instance", nil)
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
	if _, err := h.app.PollRolloutPreview(pollCtx, runner("linux-1"), "preview-instance", nil); protocol.CodeOf(err) != protocol.CodeForbidden {
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
	if _, err := h.app.PollRolloutPreview(t.Context(), runner("linux-1"), "old-runner", nil); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
}

func TestRolloutPreviewHistoricalManagerUsesActualNativeTurnFacts(t *testing.T) {
	h, as := previewFixture(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	execution := contextProjectionTurn(t, h, "manager-session", "issue:"+issue.IssueID, issue.IssueID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE runs SET role='issue_manager',native_thread_id='manager-session',native_turn_id='turn' WHERE run_id=?`, as.RunID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE runner_coordination_executions SET private_snapshot=? WHERE execution_id=?`, `{"runner_run_id":"`+as.RunID+`"}`, execution); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE main_turns SET turn_id='turn',status='completed' WHERE request_id=?`, execution)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Starting/failed entries without an actual provider turn never gain one
	// from an unrelated command's transport result.
	for _, state := range []string{store.TurnStarting, store.TurnFailed, store.TurnRejected} {
		pending := contextProjectionTurn(t, h, "manager-"+state, "issue:"+issue.IssueID, issue.IssueID)
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE runner_coordination_executions SET private_snapshot=? WHERE execution_id=?`, `{"runner_run_id":"`+as.RunID+`"}`, pending); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `UPDATE main_turns SET status=? WHERE request_id=?`, state, pending)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := h.app.ReadRolloutPreview(ctx, owner, as.RunID, protocol.RolloutPreviewQuery{})
		done <- err
	}()
	work, err := h.app.PollRolloutPreview(ctx, runner("linux-1"), "preview-instance", nil)
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

func TestRolloutPreviewShutdownPreservesPendingAndSelectedWork(t *testing.T) {
	h, as := previewFixture(t)
	// A real owner read enqueues work and closes this broker change event.
	h.app.preview.mu.Lock()
	enqueued := make(chan struct{})
	h.app.preview.changed = enqueued
	h.app.preview.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	joined := make(chan struct{})
	defer func() {
		cancel()
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Error("owner preview did not stop")
		}
	}()
	result := make(chan protocol.RolloutPreview, 1)
	go func() {
		defer close(joined)
		v, err := h.app.ReadRolloutPreview(ctx, owner, as.RunID, protocol.RolloutPreviewQuery{})
		if err != nil {
			t.Error(err)
		}
		result <- v
	}()
	select {
	case <-enqueued:
	case <-time.After(5 * time.Second):
		t.Fatal("owner preview did not enqueue work")
	}
	stopping := make(chan struct{})
	close(stopping)
	if work, err := h.app.PollRolloutPreview(ctx, runner("linux-1"), "preview-instance", stopping); err != nil || work != nil {
		t.Fatal("stopped idle poll consumed work", work, err)
	}
	h.app.preview.mu.Lock()
	for _, pending := range h.app.preview.pending {
		if pending.delivered {
			t.Error("shutdown marked pending work delivered")
		}
	}
	h.app.preview.mu.Unlock()
	selectedStop := make(chan struct{})
	work, err := h.app.PollRolloutPreview(ctx, runner("linux-1"), "preview-instance", selectedStop)
	if err != nil || work == nil {
		t.Fatal(work, err)
	}
	close(selectedStop)
	reply := protocol.RolloutPreviewReply{RequestID: work.RequestID, InstanceID: work.InstanceID, Result: protocol.RolloutPreview{State: "ready", Generation: "after-stop"}}
	if err := h.app.ReplyRolloutPreview(ctx, runner("linux-1"), reply); err != nil {
		t.Fatal("shutdown discarded selected reply", err)
	}
	select {
	case got := <-result:
		if got.State != "ready" || got.Generation != "after-stop" {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("selected work did not complete owner read")
	}
}
