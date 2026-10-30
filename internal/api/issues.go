package api

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) issues(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := views.CanonicalIssueFilter{Project: query.Get("project"), State: protocol.IssueState(query.Get("state")), IncludeClosed: query.Get("include_closed") == "true", After: query.Get("after"), Limit: 50}
	filter.Board = query.Get("board") == "true"
	filter.BoardStatus = protocol.IssueWorkStatus(query.Get("board_status"))
	filter.AssigneeKind = protocol.IssueAssigneeKind(query.Get("assignee_kind"))
	filter.AssigneeRef = query.Get("assignee_ref")
	if filter.State != "" && !slices.Contains([]protocol.IssueState{protocol.IssueActive, protocol.IssuePaused, protocol.IssueCancelling, protocol.IssueCancelled, protocol.IssueClosed}, filter.State) {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "state must name an issue state"))
		return
	}
	if query.Has("parent_id") {
		id := query.Get("parent_id")
		filter.ParentID = &id
	}
	if query.Has("limit") {
		value, err := strconv.Atoi(query.Get("limit"))
		if err != nil || value < 1 || value > 100 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100"))
			return
		}
		filter.Limit = value
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.CanonicalIssues(r.Context(), q, filter)
	})
}

func (s *Server) issueNotificationSettings(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return store.GetIssueNotificationSettings(r.Context(), q)
	})
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request) {
	var revision int64
	if raw := r.URL.Query().Get("revision"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "revision must be a positive integer"))
			return
		}
		revision = value
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.CanonicalIssue(r.Context(), q, r.PathValue("id"), revision)
	})
}
