package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"net/http"
)

func (s *Server) deadlines(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Deadlines(r.Context(), q, r.URL.Query().Get("issue_id"))
	})
}
func (s *Server) deadline(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Deadline(r.Context(), q, r.PathValue("id"))
	})
}
func (s *Server) nextActions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	raw := query.Get("owner_only")
	if query.Has("owner_only") && raw != "true" && raw != "false" {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "owner_only must be a boolean"))
		return
	}
	s.read(w, r, func(p protocol.Principal, q store.Querier) (any, error) {
		if raw == "true" {
			return views.OwnerNextActions(r.Context(), q, query.Get("kind"), query.Get("issue_id"), p.Subject)
		}
		return views.NextActions(r.Context(), q, query.Get("kind"), query.Get("issue_id"))
	})
}
func (s *Server) plannerEntry(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.PlannerEntry(r.Context(), q, r.PathValue("id"))
	})
}
