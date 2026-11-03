package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"net/http"
	"strconv"
)

func (s *Server) artifactShares(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if r.URL.Query().Has("limit") {
		n, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || n < 1 || n > 100 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "share limit must be between 1 and 100"))
			return
		}
		limit = n
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		if _, err := store.GetCanonicalIssue(r.Context(), q, r.PathValue("id")); err != nil {
			return nil, err
		}
		return store.ListArtifactShares(r.Context(), q, r.PathValue("id"), r.URL.Query().Get("after"), r.URL.Query().Get("status"), limit)
	})
}
func (s *Server) artifactShare(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return store.GetArtifactShare(r.Context(), q, r.PathValue("id"))
	})
}
