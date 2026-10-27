package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHandoffSettingsRevisionAndQueuedOwnerCommand(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	initial, err := h.app.HandoffSettings(ctx)
	if err != nil || initial.Revision != 0 {
		t.Fatalf("unexpected override: %+v %v", initial, err)
	}
	env := h.env("conversation.set_handoff_settings", protocol.Target{Type: "conversation"}, protocol.SetConversationHandoffSettingsPayload{IdleMinutes: 0}, rev(0))
	first := h.exec(p, env, SetConversationHandoffSettings{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	replay := h.exec(p, env, SetConversationHandoffSettings{})
	if string(first.Result) != string(replay.Result) || h.count("conversation_handoff_settings") != 1 {
		t.Fatalf("setting replay changed: %+v", replay)
	}
	stale := h.exec(p, h.env(env.Operation, env.Target, protocol.SetConversationHandoffSettingsPayload{IdleMinutes: 50}, rev(0)), SetConversationHandoffSettings{})
	if stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(stale)
	}
	settings, err := h.app.ConversationSettings(ctx)
	if err != nil || settings.Revision != 0 {
		t.Fatalf("handoff changed model settings: %+v %v", settings, err)
	}
	command := h.env("conversation.handoff", env.Target, protocol.ConversationHandoffPayload{PrepareOnly: true}, nil)
	for range 2 {
		if op := h.exec(p, command, ConversationHandoff{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if h.count("outbox WHERE destination='main_thread' AND operation='conversation.handoff'") != 1 {
		t.Fatal("owner replay queued duplicate maintenance")
	}
	if _, err := h.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}, h.env(env.Operation, env.Target, protocol.SetConversationHandoffSettingsPayload{IdleMinutes: 50}, rev(1)), SetConversationHandoffSettings{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("agent could change policy: %v", err)
	}
	if snap, err := h.app.HandoffSnapshot(ctx, "main"); err != nil || snap.Pending {
		t.Fatalf("maintenance outbox counted as user pending: %+v %v", snap, err)
	}
}

func TestHandoffCoverageIncludesInPlaceMessagesToolsAndTurns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema-a", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: before, Body: "initial", FormatVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	after, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil || !saved.Snapshot.Covers(after) {
		t.Fatalf("summary invalidated itself: %+v %v", after, err)
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "work"}, nil), SubmitMessage{})
	var msg struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &msg); err != nil {
		t.Fatal(err)
	}
	pending, _ := h.app.HandoffSnapshot(ctx, "main")
	if !pending.Pending || pending.Version <= before.Version || before.Covers(pending) {
		t.Fatalf("pending message omitted: %+v", pending)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error { _, err := tx.Exec(ctx, `UPDATE outbox SET status='delivered'`); return err }); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(ctx, store.MainTurn{RequestID: "request-turn", MessageID: msg.MessageID, ThreadID: "source", TurnID: "turn-1", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	active, _ := h.app.HandoffSnapshot(ctx, "main")
	if !active.Active {
		t.Fatal("active turn omitted")
	}
	h.associate(mainAgent, msg.MessageID)
	commitFixtureTurn(t, h, "request-turn")
	if err := h.app.PutMainTurn(ctx, store.MainTurn{RequestID: "request-turn", MessageID: msg.MessageID, ThreadID: "source", TurnID: "turn-1", Status: store.TurnCompleted}); err != nil {
		t.Fatal(err)
	}
	complete, _ := h.app.HandoffSnapshot(ctx, "main")
	if complete.Active || complete.LastCompletedTurnID != "turn-1" || complete.Version <= active.Version {
		t.Fatalf("turn end omitted: %+v", complete)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE conversation_messages SET body='edited in place' WHERE message_id=?`, msg.MessageID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	edit, _ := h.app.HandoffSnapshot(ctx, "main")
	if edit.MaxSeq != complete.MaxSeq || edit.Version <= complete.Version {
		t.Fatalf("in-place change lost: before=%+v after=%+v", complete, edit)
	}
	main := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "source"}
	h.exec(main, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{TurnID: "turn-1", ItemID: "call-1", Tool: "tasks_list", Status: protocol.ToolRunning}, nil), RecordToolUse{})
	running, _ := h.app.HandoffSnapshot(ctx, "main")
	h.exec(main, h.env("conversation.record_tool_use", protocol.Target{Type: "conversation"}, recordToolUsePayload{TurnID: "turn-1", ItemID: "call-1", Tool: "tasks_list", Status: protocol.ToolCompleted, Output: "result"}, nil), RecordToolUse{})
	result, _ := h.app.HandoffSnapshot(ctx, "main")
	if result.MaxSeq != running.MaxSeq || result.Version <= running.Version {
		t.Fatalf("tool result change lost: %+v %+v", running, result)
	}
	stale, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: complete, Body: "finished after owner edit", FormatVersion: 1})
	if err != nil || stale.Snapshot.Covers(result) {
		t.Fatalf("late summary claimed newer content: %+v %v", stale, err)
	}
}

func TestHandoffPhasesPreserveLegacyHistoryAndQueuedMessages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.app.SetMainThread(ctx, "main", "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.BindMainThread(ctx, "main", "legacy", "new-schema", "legacy.rollout"); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("legacy hash silently adopted: %v", err)
	}
	if err := h.app.RecordMainThreadLocation(ctx, "main", "legacy", "legacy.rollout"); err != nil {
		t.Fatal(err)
	}
	legacy, _, err := h.app.MainThreadBinding(ctx, "main")
	if err != nil || legacy.ToolsetHash != "" || legacy.Generation != 0 || legacy.RolloutPath != "legacy.rollout" {
		t.Fatalf("legacy location adopted tools: %+v %v", legacy, err)
	}
	snap, _ := h.app.HandoffSnapshot(ctx, "main")
	intent, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "main", SourceThreadID: "legacy", SourceGeneration: 0, SourceVersion: snap.Version, ToolsetHash: "schema-new", Prompt: "handoff context"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "main", SourceThreadID: "legacy", SourceGeneration: 0, ToolsetHash: "schema-new", Prompt: "newer context"})
	if err != nil || replay.ID != intent.ID || replay.Prompt != intent.Prompt {
		t.Fatalf("intent replay changed: %+v %v", replay, err)
	}
	if _, err := h.app.CommitHandoff(ctx, intent.ID); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("unconfirmed target committed: %v", err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "prepared", "creating"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordHandoffTarget(ctx, intent.ID, "target", "target.rollout"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "created", "injecting"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "injecting", "injected"); err != nil {
		t.Fatal(err)
	}
	h.exec(protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "queued during handoff"}, nil), SubmitMessage{})
	binding, err := h.app.CommitHandoff(ctx, intent.ID)
	if err != nil || binding.ThreadID != "target" || binding.Generation != 1 || len(binding.Ancestors) != 1 || binding.Ancestors[0].ThreadID != "legacy" || binding.Ancestors[0].RolloutPath != "legacy.rollout" {
		t.Fatalf("switch or ancestry lost: %+v %v", binding, err)
	}
	current, ok, err := h.app.MainThread(ctx, "main")
	if err != nil || !ok || current != "target" {
		t.Fatalf("old authority not updated: %s %v", current, err)
	}
	if h.count("conversation_messages WHERE body='queued during handoff'") != 1 || h.count("outbox WHERE operation='main_thread.handle_message'") != 1 {
		t.Fatal("handoff resent or removed queued messages")
	}
	if _, err := h.app.CommitHandoff(ctx, intent.ID); err != nil {
		t.Fatalf("commit replay failed: %v", err)
	}
}

func TestSummaryBackoffAndStalenessSurviveAnotherAppReader(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	snap, _ := h.app.HandoffSnapshot(ctx, "main")
	if ok, err := h.app.ClaimSummaryAttempt(ctx, snap, 15*time.Minute.Milliseconds()); err != nil || !ok {
		t.Fatalf("first claim: %t %v", ok, err)
	}
	if err := h.app.FinishSummaryAttempt(ctx, snap, false); err != nil {
		t.Fatal(err)
	}
	other := New(h.store, h.ids, h.clock.Now)
	if ok, err := other.ClaimSummaryAttempt(ctx, snap, 15*time.Minute.Milliseconds()); err != nil || ok {
		t.Fatalf("restart forgot backoff: %t %v", ok, err)
	}
	h.clock.Advance(15 * time.Minute)
	if ok, err := other.ClaimSummaryAttempt(ctx, snap, 15*time.Minute.Milliseconds()); err != nil || !ok {
		t.Fatalf("backoff expiry: %t %v", ok, err)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: snap, Body: "ready", FormatVersion: 1}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour)
	if ok, err := other.ClaimSummaryAttempt(ctx, snap, 0); err != nil || ok {
		t.Fatalf("successful source regenerated: %t %v", ok, err)
	}
}

func TestHandoffSettingsRejectOutOfRangeWithoutChangingRevision(t *testing.T) {
	h := newHarness(t)
	p := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	for _, minutes := range []int64{-1, 10081} {
		op := h.exec(p, h.env("conversation.set_handoff_settings", protocol.Target{Type: "conversation"}, protocol.SetConversationHandoffSettingsPayload{IdleMinutes: minutes}, rev(0)), SetConversationHandoffSettings{})
		if op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
			t.Fatalf("invalid range accepted: %+v", op)
		}
	}
	v, err := h.app.HandoffSettings(context.Background())
	if err != nil || v.Revision != 0 {
		t.Fatalf("invalid setting changed policy: %+v %v", v, err)
	}
	op := h.exec(p, h.env("conversation.set_handoff_settings", protocol.Target{Type: "conversation"}, protocol.SetConversationHandoffSettingsPayload{IdleMinutes: 10080}, rev(0)), SetConversationHandoffSettings{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestHandoffUnknownAndStaleSourceCannotAdvance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema-old", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	request := store.ThreadHandoff{Instance: "main", SourceThreadID: "source", SourceGeneration: 1, ToolsetHash: "schema-new", Prompt: "context"}
	intent, err := h.app.PrepareHandoff(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "prepared", "creating"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "creating", "unknown"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "unknown", "prepared"); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("uncertain create blindly replayed: %v", err)
	}
	replay, err := h.app.PrepareHandoff(ctx, request)
	if err != nil || replay.ID != intent.ID || replay.Phase != "unknown" {
		t.Fatalf("uncertain intent recreated: %+v %v", replay, err)
	}
	current, _, err := h.app.MainThreadBinding(ctx, "main")
	if err != nil || current.ThreadID != "source" {
		t.Fatalf("unknown switched authority: %+v %v", current, err)
	}
}

func TestHandoffCommitRejectsActiveOrChangedSourceAtomically(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "schema-old", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	intent, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "main", SourceThreadID: "source", SourceGeneration: 1, ToolsetHash: "schema-new", Prompt: "context"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "prepared", "creating"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordHandoffTarget(ctx, intent.ID, "target", "target.rollout"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "created", "injecting"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, intent.ID, "injecting", "injected"); err != nil {
		t.Fatal(err)
	}
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "next"}, nil), SubmitMessage{})
	var msg struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(op.Result, &msg)
	turn := store.MainTurn{RequestID: "active-request", MessageID: msg.MessageID, ThreadID: "source", TurnID: "turn", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.CommitHandoff(ctx, intent.ID); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("active source replaced: %v", err)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SetMainThread(ctx, "main", "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.CommitHandoff(ctx, intent.ID); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatalf("changed source replaced: %v", err)
	}
	if h.count("main_thread_bindings WHERE thread_id='target'") != 0 {
		t.Fatal("failed commit leaked target binding")
	}
}

func TestHandoffActivityStartsIdleWindowWithoutSummaryOrResumeReset(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "old", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	first, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if first.LastActivityMillis != h.clock.Now().UnixMilli() {
		t.Fatalf("fresh binding has stale activity: %+v", first)
	}
	h.clock.Advance(time.Hour)
	job, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "main", SourceThreadID: "source", SourceGeneration: 1, SourceVersion: first.Version, ToolsetHash: "new", Prompt: "reference"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, job.ID, "prepared", "creating"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordHandoffTarget(ctx, job.ID, "target", "target.rollout"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, job.ID, "created", "injecting"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, job.ID, "injecting", "injected"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.CommitHandoff(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	switched, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if switched.LastActivityMillis != h.clock.Now().UnixMilli() || switched.Version != first.Version || first.Covers(switched) {
		t.Fatalf("handoff idle/coverage changed incorrectly: first=%+v switched=%+v", first, switched)
	}
	h.clock.Advance(time.Minute)
	if err := h.app.RecordMainThreadLocation(ctx, "main", "target", "target.refreshed.rollout"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: switched, Body: "private summary", FormatVersion: 2}); err != nil {
		t.Fatal(err)
	}
	resumed, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.LastActivityMillis != switched.LastActivityMillis || !switched.Covers(resumed) {
		t.Fatalf("location/summary reset idle: %+v", resumed)
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "new owner message"}, nil), SubmitMessage{})
	active, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if active.LastActivityMillis != h.clock.Now().UnixMilli() {
		t.Fatalf("new owner activity lost: %+v", active)
	}
}
