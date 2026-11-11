package store

import "context"

// businessOperation is audit provenance attached to the actual domain revision.
func businessOperation(ctx context.Context) string {
	source, _ := ctx.Value(writeSourceKey{}).(WriteSource)
	return source.Operation
}
