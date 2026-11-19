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
	filter.GroupReview = query.Get("group_review") == "true"
	if raw := query.Get("group_review"); query.Has("group_review") && (raw != "false" && raw != "true" || filter.GroupReview && !filter.Board) {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "group_review must be a boolean and requires board=true when enabled"))
		return
	}
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
	if raw := query.Get("waiting_on_me"); query.Has("waiting_on_me") && (raw != "false" && raw != "true" || raw == "true" && !filter.Board) {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "waiting_on_me must be a boolean and requires board=true when enabled"))
		return
	}
	s.read(w, r, func(p protocol.Principal, q store.Querier) (any, error) {
		if query.Get("waiting_on_me") == "true" {
			filter.WaitingOnOwner = p.Subject
		}
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

func (s *Server) issueActivity(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	query := views.IssueActivityQuery{Readable: values.Get("view") == "readable"}
	if values.Get("view") != "" && !query.Readable {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "unknown activity view"))
		return
	}
	for key, dest := range map[string]*int64{"before": &query.Before, "since": &query.Since, "through": &query.Through, "from": &query.From} {
		n, err := intParam(values.Get(key))
		if err != nil || n < 0 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid activity cursor %s", key))
			return
		}
		*dest = n
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.ReadIssueActivity(r.Context(), q, r.PathValue("id"), query)
	})
}
func (s *Server) issueActivityNotification(w http.ResponseWriter, r *http.Request) {
	position, positionErr := intParam(r.PathValue("position"))
	after, afterErr := intParam(r.URL.Query().Get("after"))
	if positionErr != nil || position <= 0 || afterErr != nil || after < 0 {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid activity position or notification cursor"))
		return
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		out, err := views.IssueEventNotification(r.Context(), q, r.PathValue("id"), position, after)
		return out, notFound(err)
	})
}

func (s *Server) issueResults(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.IssueResults(r.Context(), q, r.PathValue("id"), r.URL.Query().Get("after"))
	})
}

func (s *Server) issueDeliveryReviews(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100"))
			return
		}
		limit = value
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.SharedDeliveryReviews(r.Context(), q, r.PathValue("id"), r.URL.Query().Get("delivery_id"), r.URL.Query().Get("after"), limit)
	})
}
