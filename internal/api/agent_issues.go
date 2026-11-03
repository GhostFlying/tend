package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) agentIssues(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := strconv.Atoi(defaultAgentIssueParam(query.Get("limit"), "25"))
	if err != nil || limit < 1 || limit > 100 {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100"))
		return
	}
	revision, err := strconv.ParseInt(defaultAgentIssueParam(query.Get("agent_revision"), "0"), 10, 64)
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid agent_revision"))
		return
	}
	f := views.AgentIssueFilter{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: query.Get("environment_id"), LocalID: query.Get("local_id")}, Scope: query.Get("scope"), State: query.Get("state"), Search: query.Get("search"), After: query.Get("after"), Limit: limit, SnapshotID: query.Get("snapshot_id"), AgentRevision: revision}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) { return views.AgentIssues(r.Context(), q, f) })
}

func defaultAgentIssueParam(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
