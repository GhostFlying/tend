package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// NativeEntry reads no provider and sends no control command. An owner may use
// existing takeover/resume commands after reviewing the current task revision.
func (a *App) NativeEntry(ctx context.Context, p protocol.Principal, id, provider string) (protocol.NativeEntry, error) {
	v := protocol.NativeEntry{RunID: id, ControllerObservation: "unknown", Reason: "native_identity_not_reported"}
	if err := allowKinds(p, protocol.PrincipalOwner); err != nil {
		return v, err
	}
	if provider != "" && provider != "orca" && provider != "cove" {
		return v, protocol.Errorf(protocol.CodeInvalidRequest, "unsupported native entry provider")
	}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		run, err := store.GetRun(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		task, err := store.GetTask(ctx, tx.Q(), run.TaskID)
		if err != nil {
			return err
		}
		v.TaskID, v.HostID, v.RunObserved = run.TaskID, run.HostID, run.ObservedState
		v.ControlMode, v.ControlRevision = task.ControlMode, task.ControlRevision
		if pin, err := store.GetRunExecution(ctx, tx.Q(), id); err == nil {
			v.Execution = pin
			v.Worker = pin.Worker
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if v.Worker == "" {
			host, err := store.GetHost(ctx, tx.Q(), run.HostID)
			if err != nil {
				return err
			}
			v.Worker = host.Worker
		}
		var resourceID string
		err = tx.Q().QueryRowContext(ctx, `SELECT workspace_id FROM workspace_runs WHERE run_id=?`, id).Scan(&resourceID)
		var workspace store.WorkspaceRow
		if errors.Is(err, sql.ErrNoRows) {
			err = store.ErrNotFound
		} else if err == nil {
			workspace, err = store.GetWorkspace(ctx, tx.Q(), resourceID)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		actual := "unknown"
		if err == nil && (workspace.BackendID == "orca" || workspace.Kind == "orca") {
			actual = "orca"
		}
		if provider == "cove" {
			v.Provider = "cove"
			v.Reason = "adapter_not_implemented"
			return nil
		}
		v.Provider = actual
		v.AdapterImplemented = actual == "orca"
		v.WorkspaceID, v.WorkspacePath = workspace.WorkspaceID, workspace.Path
		v.ProviderID, v.ProviderInstanceID = workspace.ProviderID, workspace.ProviderInstanceID
		v.WorkspaceRecorded = workspace.ProviderID != "" && workspace.ProviderInstanceID != "" && workspace.Path != ""
		if actual != "orca" {
			v.Reason = "native_entry_not_supported_for_workspace"
		}
		var raw string
		err = tx.Q().QueryRowContext(ctx, `SELECT body_json,seq,created_at FROM run_logs WHERE run_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, id, protocol.EventNativeIdentity).Scan(&raw, &v.IdentitySequence, &v.IdentityRecordedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var identity protocol.NativeIdentity
		if json.Unmarshal([]byte(raw), &identity) != nil || identity.Provider != "orca" {
			v.Reason = "native_identity_unavailable"
			return nil
		}
		// Historical legacy resources need not have a named backend kind. The
		// original run's actual provider report is authoritative navigation data.
		v.Provider, v.AdapterImplemented = "orca", true
		v.Identity = &identity
		v.ControllerObservation = identity.ControllerObservation
		if identity.ProviderID != "" && workspace.ProviderID != "" && identity.ProviderID != workspace.ProviderID {
			v.Reason = "workspace_identity_mismatch"
			return nil
		}
		v.SessionRecorded = identity.State == "known" && identity.Terminal != "" && identity.Pane != "" && identity.SessionID != ""
		if v.SessionRecorded {
			v.Reason = ""
		} else if identity.Reason != "" {
			v.Reason = identity.Reason
		}
		// A recorded locator is navigation/history, not native control authority.
		return nil
	})
	return v, err
}
