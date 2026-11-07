package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestDirectNativeEntrySeparatesLocatorControlAndStopVerification(t *testing.T) {
	h, as := messageRun(t)
	workspace := h.resourceForTask(as.TaskID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE workspaces SET backend_id='git',path='original-workspace' WHERE workspace_id=?`, workspace.WorkspaceID); err != nil {
			return err
		}
		return tx.InsertRunExecution(t.Context(), as.RunID, protocol.RunExecutionPin{Worker: protocol.WorkerAppServer, BackendID: "git", Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "linux-1"}})
	}); err != nil {
		t.Fatal(err)
	}
	read := func() protocol.NativeEntry {
		v, err := h.app.NativeEntry(t.Context(), owner, as.RunID, "appserver")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := read(); v.ResumeState != "unavailable" || v.SessionRecorded || !v.WorkspaceRecorded {
		t.Fatal(v)
	}
	raw, _ := json.Marshal(protocol.NativeIdentity{Provider: "appserver", State: "known", SessionID: "original-session", TurnID: "original-turn", ControllerObservation: "managed"})
	if _, err := h.app.RecordRunEvents(t.Context(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, as.RunID, []protocol.RunEvent{{Seq: 1, Kind: protocol.EventNativeIdentity, Body: raw}}); err != nil {
		t.Fatal(err)
	}
	if v := read(); v.ResumeState != "requires_takeover" || !v.SessionRecorded || v.Identity.SessionID != "original-session" {
		t.Fatal(v)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET control_mode='human_control' WHERE task_id=?`, as.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if v := read(); v.ResumeState != "requires_local_verification" || v.ResumeReason != "verify_original_session_workspace_and_stopped_writer" {
		t.Fatal("locator falsely proves stopped execution", v)
	}
	v, err := h.app.NativeEntry(t.Context(), owner, as.RunID, "orca")
	if err != nil || v.Reason != "workspace_provider_mismatch" || v.SessionRecorded {
		t.Fatal(v, err)
	}
}

func TestNativeEntryExplicitProviderPreservesHistoricalUnlabelledWorkspace(t *testing.T) {
	h, as := messageRun(t)
	raw, _ := json.Marshal(protocol.NativeIdentity{Provider: "orca", ProviderID: "original", Terminal: "terminal", Pane: "pane", SessionID: "session", State: "known"})
	if _, err := h.app.RecordRunEvents(t.Context(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, as.RunID, []protocol.RunEvent{{Seq: 1, Kind: protocol.EventNativeIdentity, Body: raw}}); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"", "orca"} {
		v, err := h.app.NativeEntry(t.Context(), owner, as.RunID, provider)
		if err != nil || !v.SessionRecorded || v.Identity == nil || v.Identity.SessionID != "session" {
			t.Fatal(v, err)
		}
	}
}
