package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNativeIdentityOwnerViewUsesOriginalRunLatestSequenceAndHonestUnknown(t *testing.T) {
	h, as := messageRun(t)
	originalTask := h.task(as.TaskID)
	run := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
	identity := protocol.NativeIdentity{Provider: "orca", ProviderID: "original-worktree", Terminal: "original-terminal", Pane: "original-pane", SessionID: "original-session", State: "known", ControllerObservation: "unknown"}
	event := func(seq int64, value protocol.NativeIdentity) protocol.RunEvent {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.RunEvent{Seq: seq, Kind: protocol.EventNativeIdentity, Body: raw}
	}
	latest := event(20, identity)
	if result, err := h.app.RecordRunEvents(t.Context(), run, as.RunID, []protocol.RunEvent{latest}); err != nil || result.Accepted != 1 {
		t.Fatal(result, err)
	}
	// Late delivery retains its raw historical fact without replacing newer state.
	stale := identity
	stale.SessionID = "stale-session"
	if _, err := h.app.RecordRunEvents(t.Context(), run, as.RunID, []protocol.RunEvent{event(10, stale)}); err != nil {
		t.Fatal(err)
	}
	if result, err := h.app.RecordRunEvents(t.Context(), run, as.RunID, []protocol.RunEvent{latest}); err != nil || result.Accepted != 0 {
		t.Fatal("same event was replayed", result, err)
	}
	v, err := h.app.NativeEntry(t.Context(), owner, as.RunID, "")
	if err != nil || !v.SessionRecorded || v.Identity == nil || v.Identity.SessionID != "original-session" || v.IdentitySequence != 20 {
		t.Fatal(v, err)
	}
	for _, p := range []protocol.Principal{mainAgent, run, {Kind: protocol.PrincipalBackgroundAgent, Subject: "consultation", Thread: "thread", ConversationID: "consultation"}} {
		if _, err := h.app.NativeEntry(t.Context(), p, as.RunID, ""); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("non-owner accessed native navigation", p, err)
		}
	}
	identity.State, identity.Reason = "unknown", "native_observation_unconfirmed"
	if _, err := h.app.RecordRunEvents(t.Context(), run, as.RunID, []protocol.RunEvent{event(30, identity)}); err != nil {
		t.Fatal(err)
	}
	v, err = h.app.NativeEntry(t.Context(), owner, as.RunID, "")
	if err != nil || v.SessionRecorded || v.Identity.SessionID != "original-session" || v.Reason != "native_observation_unconfirmed" {
		t.Fatal("unknown observation became live control", v, err)
	}
	if task := h.task(as.TaskID); task.ControlRevision != originalTask.ControlRevision || task.ControlMode != originalTask.ControlMode || task.SpecVersion != originalTask.SpecVersion {
		t.Fatal("identity fact changed task agreement/control")
	}
}

func TestNativeIdentityOldRunKeepsOriginalResourceAfterOwnerStartsOver(t *testing.T) {
	h, as := messageRun(t)
	resource := h.resourceForTask(as.TaskID)
	identity := protocol.NativeIdentity{Provider: "orca", ProviderID: "original-native-worktree", Terminal: "original-native-terminal", Pane: "original-pane", SessionID: "original-native-session", State: "known", ControllerObservation: "unknown"}
	raw, _ := json.Marshal(identity)
	if _, err := h.app.RecordRunEvents(t.Context(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, as.RunID, []protocol.RunEvent{{Seq: 1, Kind: protocol.EventNativeIdentity, Body: raw}}); err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(as.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.withHosts("linux-2")
	if len(h.checkWorkspaces()) != 1 {
		t.Fatal("missing actual unavailable-workspace decision")
	}
	decisions := h.openDecisions(as.TaskID)
	if len(decisions) != 1 {
		t.Fatal(decisions)
	}
	if result := h.answer(decisions[0], domain.StartOverOption); result.Effect == nil || !result.Effect.Applied {
		t.Fatal(result)
	}
	next, ok, _ := h.claim("linux-2")
	if !ok || next.RunID == as.RunID || next.Role != protocol.RoleImplement {
		t.Fatal("replacement run was not allocated", next, ok)
	}
	old, err := h.app.NativeEntry(t.Context(), owner, as.RunID, "")
	if err != nil || old.WorkspaceID != resource.WorkspaceID || old.Identity == nil || old.Identity.SessionID != "original-native-session" {
		t.Fatal("old run followed the replacement binding", old, err)
	}
	current, err := h.app.NativeEntry(t.Context(), owner, next.RunID, "")
	if err != nil || current.Identity != nil || current.SessionRecorded || current.WorkspaceID == old.WorkspaceID {
		t.Fatal("replacement inherited old native session", current, err)
	}
}
