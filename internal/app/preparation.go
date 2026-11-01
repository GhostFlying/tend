package app

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// validatePreparationFailure binds evidence to the frozen run contract. It
// accepts no free-form stderr or suggested authorization as proof of a remedy.
func validatePreparationFailure(ctx context.Context, q store.Querier, rc runContext, p protocol.ReleasePayload) error {
	f := p.Preparation
	if f == nil {
		return nil
	}
	invalid := func() error {
		return protocol.Errorf(protocol.CodeInvalidRequest, "preparation diagnosis does not match the run's frozen workspace contract")
	}
	frozen, err := runSpec(ctx, q, rc)
	if err != nil {
		return err
	}
	if p.Reason != "workspace_prepare_failed" || rc.run.Role != protocol.RoleImplement ||
		f.Phase != protocol.PreparationPhaseWorkspace || f.Code != protocol.PreparationInheritedChanges || f.WorkerStarted ||
		f.RequiredField != protocol.PreparationIncludeField || frozen.Workspace.ModeValue() != domain.WorkspaceReuse ||
		frozen.Workspace.IncludeExistingChanges || !frozen.RequiresDelivery(domain.DeliveryCommit) || f.SourceTaskID != frozen.Workspace.SourceTaskID ||
		f.ChangeCount < 0 || len(f.Changes) > protocol.MaxPreparationChanges || f.ChangeCount < len(f.Changes) || f.Truncated != (f.ChangeCount > len(f.Changes)) {
		return invalid()
	}
	ws, err := store.GetWorkspaceByTask(ctx, q, rc.task.TaskID)
	if err != nil {
		return err
	}
	if f.WorkspaceID != ws.WorkspaceID {
		return invalid()
	}
	for _, change := range f.Changes {
		mount := false
		for _, m := range frozen.Workspace.Mounts {
			mount = mount || m.Path == change.Mount
		}
		_, hashErr := hex.DecodeString(change.HEAD)
		if !mount || len(change.Path) == 0 || len(change.Path) > 512 || !utf8.ValidString(change.Path) || strings.ContainsFunc(change.Path, unicode.IsControl) || strings.HasPrefix(change.Path, "/") ||
			strings.Contains("/"+change.Path+"/", "/../") || len(change.Status) != 2 || strings.Trim(change.Status, " MADRCUT?!") != "" || hashErr != nil || len(change.HEAD) != 40 && len(change.HEAD) != 64 {
			return invalid()
		}
	}
	return nil
}

func preparationOwnerCurrent(ctx context.Context, q store.Querier, rc runContext) (bool, error) {
	// Admission to repair is stricter than accepting the final fact. A late
	// journaled release must still settle an expired/reconciled or old-spec run.
	if rc.run.ObservedState != protocol.RunStarting {
		return false, nil
	}
	contract, err := store.GetTaskOwnerContract(ctx, q, rc.task.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current, err := store.TaskOwnerContractCurrent(ctx, q, rc.task.TaskID)
	if err != nil || !current {
		return false, err
	}
	current, err = store.RunIssueResponsibilityCurrent(ctx, q, rc.run.RunID)
	if err != nil || !current {
		return false, err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, contract.IssueID)
	return issue.State == protocol.IssueActive && issue.AssignmentState == "active" && rc.task.ControlMode == protocol.ControlManaged && desiredFor(rc.run, rc.action, rc.task) == protocol.DesiredRun, err
}
