package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) issueContext(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		after, err := intParam(r.URL.Query().Get("after"))
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid source cursor")
		}
		return views.CanonicalIssueContext(r.Context(), q, r.PathValue("id"), after)
	})
}
func (s *Server) issueFollowups(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.CanonicalIssueFollowups(r.Context(), q, r.PathValue("id"), r.URL.Query().Get("after"), s.app.FollowupsEnabled())
	})
}
func (s *Server) issueFollowupHistory(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		after, err := intParam(r.URL.Query().Get("after_revision"))
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid revision cursor")
		}
		return views.CanonicalIssueFollowupHistory(r.Context(), q, r.PathValue("id"), r.PathValue("rule"), r.URL.Query().Get("after_occurrence"), after)
	})
}
func (s *Server) issueDeliveries(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.CanonicalIssueDeliveries(r.Context(), q, r.PathValue("id"))
	})
}
