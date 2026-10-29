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
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.NextActions(r.Context(), q, r.URL.Query().Get("kind"), r.URL.Query().Get("issue_id"))
	})
}
func (s *Server) plannerEntry(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.PlannerEntry(r.Context(), q, r.PathValue("id"))
	})
}
