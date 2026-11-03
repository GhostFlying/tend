package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func concurrencyQuery(r *http.Request) (protocol.ConcurrencyQuery, error) {
	v := r.URL.Query()
	q := protocol.ConcurrencyQuery{GroupID: v.Get("group_id"), Search: v.Get("search"), EnvironmentID: v.Get("environment_id"), RuntimeID: v.Get("runtime_id"), IssueID: v.Get("issue_id"), SessionID: v.Get("session_id"), Reason: v.Get("reason"), After: v.Get("after")}
	var err error
	if v.Get("limit") != "" {
		q.Limit, err = strconv.Atoi(v.Get("limit"))
		if err != nil {
			return q, protocol.Errorf(protocol.CodeInvalidRequest, "invalid page limit")
		}
	}
	for name, dest := range map[string]*int64{"since": &q.Since, "until": &q.Until} {
		if v.Get(name) != "" {
			*dest, err = strconv.ParseInt(v.Get(name), 10, 64)
			if err != nil {
				return q, protocol.Errorf(protocol.CodeInvalidRequest, "invalid time range")
			}
		}
	}
	return q, nil
}
func (s *Server) concurrencyGroups(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	q, err := concurrencyQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.app.ConcurrencyGroups(r.Context(), q)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
func (s *Server) concurrencyGroup(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	group, err := s.app.ConcurrencyGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, group)
}
func (s *Server) concurrencyHistory(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	q, err := concurrencyQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.app.ConcurrencyHistory(r.Context(), q)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
