package nativeusage

import (
	"context"
	"errors"
	"os"

	"github.com/GhostFlying/tend/internal/protocol"
)

var ErrIdentity = errors.New("native usage: history identity mismatch")
var ErrHistoryUnavailable = errors.New("native usage: registered history is unavailable")

func CollectionError(err error) string {
	switch {
	case errors.Is(err, ErrIdentity):
		return "identity_mismatch"
	case errors.Is(err, ErrHistoryUnavailable), errors.Is(err, os.ErrNotExist):
		return "history_unavailable"
	case errors.Is(err, os.ErrPermission):
		return "history_permission"
	default:
		return "history_read_failed"
	}
}

// CommitQueue retains the exact read result while persistence is unavailable.
// Retry never rediscovers files or recreates an upload Envelope. Its owner is
// the collector goroutine; source revision checks discard stale work atomically.
type CommitQueue map[string]func(context.Context) error

func (q CommitQueue) Commit(ctx context.Context, key string, commit func(context.Context) error) error {
	err := commit(ctx)
	if err != nil && protocol.CodeOf(err) != protocol.CodeStaleRevision {
		q[key] = commit
	} else {
		delete(q, key)
	}
	return err
}

func (q CommitQueue) Retry(ctx context.Context, key string) (bool, error) {
	commit, ok := q[key]
	if !ok {
		return false, nil
	}
	err := q.Commit(ctx, key, commit)
	if protocol.CodeOf(err) == protocol.CodeStaleRevision {
		err = nil
	}
	return true, err
}

func (q CommitQueue) Retain(keys map[string]bool) {
	for key := range q {
		if !keys[key] {
			delete(q, key)
		}
	}
}
