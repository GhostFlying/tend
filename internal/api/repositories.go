package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"net/http"
)

func (s *Server) repository(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		repo, err := store.GetRepo(r.Context(), q, r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		revision, err := store.RepositoryRevision(r.Context(), q, repo.Repo)
		if err != nil {
			return nil, err
		}
		return protocol.RepositoryRegistration{Repo: repo.Repo, RemoteURL: repo.RemoteURL, BaseCandidates: repo.BaseCandidates, Retired: repo.Retired, ControlRevision: revision}, nil
	})
}
