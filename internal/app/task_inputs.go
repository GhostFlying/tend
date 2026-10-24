package app

import (
	"context"
	"encoding/json"
	"os"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// freezeTaskInputs resolves caller IDs into server-owned immutable metadata.
// Existing references keep the metadata frozen by the previous spec, even if
// the original is attached to a chat message later. Relations never copy inputs.
func freezeTaskInputs(ctx context.Context, q store.Querier, spec *domain.Spec, previous []protocol.TaskInput) error {
	frozen := make(map[string]protocol.TaskInput, len(previous))
	for _, input := range previous {
		frozen[input.FileID] = input
	}
	seen := make(map[string]bool, len(spec.Inputs))
	for i, input := range spec.Inputs {
		if !protocol.ValidID(protocol.PrefixFile, input.FileID) || seen[input.FileID] {
			return protocol.Errorf(protocol.CodeInvalidRequest, "task inputs require distinct valid file_id values")
		}
		seen[input.FileID] = true
		if retained, ok := frozen[input.FileID]; ok {
			spec.Inputs[i] = retained
			continue
		}
		f, err := store.GetConversationAttachment(ctx, q, input.FileID)
		if err != nil {
			return errNotFound(err, "input attachment does not exist")
		}
		source, err := store.AttachmentSourceMessage(ctx, q, input.FileID)
		if err != nil {
			return err
		}
		spec.Inputs[i] = protocol.TaskInput{FileID: f.FileID, Name: f.Name, MediaType: f.MediaType, Size: f.Size, SHA256: f.SHA256, SourceMessageID: source}
	}
	return nil
}

func runtimeSupportsTaskInputs(runtime *protocol.RunnerRuntime) bool {
	return runtime != nil && slices.Contains(runtime.Capabilities, protocol.CapabilityTaskInputs)
}

// OpenRunFile serves the immutable inputs of the run's exact spec version,
// or its existing deliverable permissions. It never uses owner credentials.
func (a *App) OpenRunFile(ctx context.Context, p protocol.Principal, id string) (protocol.ArtifactFile, *os.File, error) {
	if p.Kind != protocol.PrincipalRun {
		return protocol.ArtifactFile{}, nil, protocol.Errorf(protocol.CodeForbidden, "run identity is required")
	}
	var input *protocol.TaskInput
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rc, err := loadRun(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		if p.Role != rc.run.Role {
			return protocol.Errorf(protocol.CodeForbidden, "run role does not match")
		}
		body, err := store.GetSpec(ctx, tx.Q(), rc.task.TaskID, rc.action.TargetSpecVersion)
		if err != nil {
			return err
		}
		var spec domain.Spec
		if err := json.Unmarshal([]byte(body), &spec); err != nil {
			return err
		}
		for _, candidate := range spec.Inputs {
			if candidate.FileID == id {
				input = &candidate
				break
			}
		}
		return nil
	})
	if err != nil {
		return protocol.ArtifactFile{}, nil, err
	}
	if input != nil {
		body, err := a.store.FileStore().Open(input.SHA256)
		return protocol.ArtifactFile{FileID: input.FileID, Name: input.Name, Size: input.Size, SHA256: input.SHA256}, body, err
	}
	file, body, err := a.OpenDeliverableFile(ctx, p, id)
	return file.ArtifactFile, body, err
}

// Recovery must retain the same input support as the original claim. Otherwise
// restarting an older runner could bypass the scheduler through reassignment.
func checkInputRunRecovery(ctx context.Context, q store.Querier, host string, runtime *protocol.RunnerRuntime) error {
	if runtimeSupportsTaskInputs(runtime) {
		return nil
	}
	open, err := store.ListOpenRunsOnHost(ctx, q, host)
	if err != nil {
		return err
	}
	for _, run := range open {
		rc, err := loadRun(ctx, q, run.RunID)
		if err != nil {
			return err
		}
		if desiredFor(run, rc.action, rc.task) == protocol.DesiredStopped {
			continue
		}
		body, err := store.GetSpec(ctx, q, run.TaskID, rc.action.TargetSpecVersion)
		if err != nil {
			return err
		}
		var spec domain.Spec
		if err := json.Unmarshal([]byte(body), &spec); err != nil {
			return err
		}
		if len(spec.Inputs) > 0 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "runner requires task-inputs-v1 to recover an assigned input task")
		}
	}
	return nil
}
