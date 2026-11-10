package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// stageRetainedInput publishes only immutable bytes before the business write.
// Admission and references still require the normal transaction authorization.
func (a *App) stageRetainedInput(ctx context.Context, env *protocol.Envelope, domains ...string) (context.Context, error) {
	switch env.Operation {
	case protocol.OpRunnerRolloutArchive, protocol.OpMigrateOriginals, protocol.OpImportOriginals, "manager.native_enqueue", protocol.OpManagerNativeEvent, protocol.OpManagerNativeReply:
	default:
		return ctx, nil
	}
	if len(domains) > 0 {
		recorded := false
		err := a.store.Read(ctx, func(tx *store.ReadTx) error {
			err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM request_records WHERE domain=? AND request_id=?)`, domains[0], env.RequestID).Scan(&recorded)
			return err
		})
		if err != nil {
			return ctx, err
		}
		if recorded {
			return ctx, nil
		}
	}
	switch env.Operation {
	case protocol.OpRunnerRolloutArchive:
		if len(domains) == 0 || !strings.HasPrefix(domains[0], "runner:") {
			return ctx, protocol.Errorf(protocol.CodeForbidden, "native original requires a runner host")
		}
		var p protocol.NativeRolloutChunk
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return ctx, protocol.Errorf(protocol.CodeInvalidRequest, "invalid native original chunk")
		}
		if env.Target.Type != "run" || env.Target.ID != p.RunID {
			return ctx, protocol.Errorf(protocol.CodeInvalidRequest, "native original target differs from its Run")
		}
		prepared, err := a.store.PrepareNativeRollout(ctx, strings.TrimPrefix(domains[0], "runner:"), p)
		if err != nil {
			return ctx, err
		}
		return context.WithValue(ctx, nativeRolloutKey{}, prepared), nil

	case protocol.OpMigrateOriginals:
		var p protocol.OriginalMigration
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return ctx, protocol.Errorf(protocol.CodeInvalidRequest, "invalid original migration input")
		}
		staged, batch, err := a.store.PrepareOriginalMigration(ctx, p.Limit, p.Rescan)
		if err != nil {
			return ctx, err
		}
		return context.WithValue(staged, originalMigrationKey{}, batch), nil
	case protocol.OpImportOriginals:
		var p protocol.OriginalImport
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return ctx, protocol.Errorf(protocol.CodeInvalidRequest, "invalid original import input")
		}
		batch, err := a.store.PrepareOriginalImport(ctx, p)
		if err != nil {
			return ctx, err
		}
		return context.WithValue(ctx, originalImportKey{}, batch), nil
	case "manager.native_enqueue":
		var p protocol.ManagerNativeRequest
		if json.Unmarshal(env.Payload, &p) != nil {
			return ctx, nil
		}
		return a.store.StageOriginals(ctx, "manager-commands:"+env.Target.ID, p.Payload)
	case protocol.OpManagerNativeReply:
		var p protocol.ManagerNativeReply
		if json.Unmarshal(env.Payload, &p) != nil {
			return ctx, nil
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return ctx, err
		}
		return a.store.StageOriginals(ctx, "manager-results:"+env.Target.ID, raw)
	case protocol.OpManagerNativeEvent:
		var p protocol.ManagerNativeEvent
		if json.Unmarshal(env.Payload, &p) != nil {
			return ctx, nil
		}
		return a.store.StageOriginals(ctx, "manager-events:"+env.Target.ID, p.Body)
	}
	return ctx, nil
}
