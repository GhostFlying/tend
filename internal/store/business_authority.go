package store

import "context"

type businessOwnerKey struct{}

// WithBusinessOwner carries the authenticated owner behind a business write.
// It is scoped to the transaction context, never inferred from request history.
func WithBusinessOwner(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, businessOwnerKey{}, subject)
}

func businessOwner(ctx context.Context) string {
	owner, _ := ctx.Value(businessOwnerKey{}).(string)
	return owner
}
