package store

import "context"

// HasOwnerMessageAuthority preserves actual authorship while recognizing a
// main Agent's server-bound owner decision. Ordinary Agent narration is excluded.
func HasOwnerMessageAuthority(ctx context.Context, q Querier, id string) (bool, error) {
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owner_authority_messages WHERE message_id=?)`, id).Scan(&allowed)
	return allowed, err
}
