package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

// RepositoryRevision is zero for entries still managed by startup configuration.
func RepositoryRevision(ctx context.Context, q Querier, repo string) (int64, error) {
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT control_revision FROM online_repositories WHERE repo=?`, repo).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

// ManageRepository makes subsequent startup imports leave the catalog entry intact.
func (t *Tx) ManageRepository(ctx context.Context, repo string, revision int64) error {
	_, err := t.Exec(ctx, `INSERT INTO online_repositories(repo,control_revision) VALUES(?,?) ON CONFLICT(repo) DO UPDATE SET control_revision=excluded.control_revision`, repo, revision)
	if err != nil {
		return fmt.Errorf("store: manage repository: %w", err)
	}
	return nil
}

// addObservedRepositories includes only bindings loaded by the current runner
// whose remote identity matches the owner's catalog. Legacy static repos remain.
func addObservedRepositories(ctx context.Context, q Querier, h *HostRow) error {
	rt, err := GetHostRuntime(ctx, q, h.HostID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, bound := range rt.Runtime.Repositories {
		repo, err := GetRepo(ctx, q, bound.Repo)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if protocol.RepositoryRemoteKey(repo.RemoteURL) == bound.RemoteIdentity && !slices.Contains(h.Repos, bound.Repo) {
			h.Repos = append(h.Repos, bound.Repo)
		}
	}
	return nil
}
