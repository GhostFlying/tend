package views

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RegisteredRepos reads every ledger registration and its non-retired service
// hosts, exactly as repos_list does. Offline hosts remain service hosts (D30).
// The projection deliberately excludes remote URLs and host configuration.
func RegisteredRepos(ctx context.Context, q store.Querier) ([]protocol.RegisteredRepoView, error) {
	repos, err := store.ListRepos(ctx, q)
	if err != nil {
		return nil, err
	}
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.RegisteredRepoView, 0, len(repos))
	for _, repo := range repos {
		v := protocol.RegisteredRepoView{Repo: repo.Repo, Retired: repo.Retired, Hosts: []string{}}
		for _, host := range hosts {
			if slices.Contains(host.Repos, repo.Repo) {
				v.Hosts = append(v.Hosts, host.HostID)
			}
		}
		out = append(out, v)
	}
	return out, nil
}
