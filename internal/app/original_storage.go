package app

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type originalMigrationKey struct{}
type originalImportKey struct{}

// OriginalStorageControl changes only archival representation, never business
// state or authorization. Native commands are not replayed by this operation.
type OriginalStorageControl struct{ OperationName string }

func (c OriginalStorageControl) Operation() string { return c.OperationName }
func (c OriginalStorageControl) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c OriginalStorageControl) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.ExpectedControlRevision == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "original storage control revision is required")
	}
	var result any
	var err error
	switch c.OperationName {
	case protocol.OpMigrateOriginals:
		prepared, _ := ctx.Value(originalMigrationKey{}).(*store.OriginalMigrationBatch)
		result, err = tx.ApplyOriginalMigration(ctx, prepared, *x.Env.ExpectedControlRevision, x.Now.UnixMilli())
	case protocol.OpImportOriginals:
		prepared, _ := ctx.Value(originalImportKey{}).(*store.OriginalImportBatch)
		result, err = tx.ApplyOriginalImport(ctx, prepared, *x.Env.ExpectedControlRevision, x.Now.UnixMilli())
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown original storage operation")
	}
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: result, ProjectionOnly: true, Events: []Event{{ObjectType: "storage", ObjectID: "originals", Kind: "storage.originals_changed"}}}, nil
}
func (a *App) OriginalStorageStatus(ctx context.Context) (protocol.OriginalStorageStatus, error) {
	return a.store.OriginalStorageState(ctx)
}
func (a *App) VerifyOriginalStorage(ctx context.Context, after string, limit int) (protocol.OriginalVerification, error) {
	return a.store.VerifyOriginals(ctx, after, limit)
}
