package app

import (
	"context"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RegisterRepository adopts the named entry into the online owner catalog.
type RegisterRepository struct{}

func (RegisterRepository) Operation() string { return "repos.register" }
func (RegisterRepository) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (RegisterRepository) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	name := x.Env.Target.ID
	var p protocol.RegisterRepositoryPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if x.Env.Target.Type != "repo" || !domain.NamePattern.MatchString(name) || (strings.TrimSpace(p.RemoteURL) == "" || strings.TrimSpace(p.RemoteURL) != p.RemoteURL) || strings.HasPrefix(p.RemoteURL, "-") || strings.ContainsAny(p.RemoteURL, "\r\n\x00") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "a valid repository name and remote URL are required")
	}
	for _, base := range p.BaseCandidates {
		if !domain.ValidBranch(base) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid base candidate")
		}
	}
	revision, err := store.RepositoryRevision(ctx, tx.Q(), name)
	if err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "repository control revision is %d", revision)
	}
	old, err := store.GetRepo(ctx, tx.Q(), name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if err == nil && protocol.RepositoryRemoteKey(old.RemoteURL) != protocol.RepositoryRemoteKey(p.RemoteURL) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "register a different logical name to change repository identity")
	}
	if err := tx.UpsertRepo(ctx, store.RepoRow{Repo: name, RemoteURL: p.RemoteURL, BaseCandidates: p.BaseCandidates, Retired: p.Retired}, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if err := tx.ManageRepository(ctx, name, revision+1); err != nil {
		return Outcome{}, err
	}
	result := protocol.RepositoryRegistration{Repo: name, RemoteURL: p.RemoteURL, BaseCandidates: p.BaseCandidates, Retired: p.Retired, ControlRevision: revision + 1}
	return Outcome{Result: result, Notify: true, Events: []Event{{ObjectType: "repo", ObjectID: name, ObjectVersion: revision + 1, Kind: "repo.registered", Body: map[string]any{"retired": p.Retired}}}}, nil
}
