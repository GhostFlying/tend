package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) agentCategories(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	limit := 0
	if q.Get("limit") != "" {
		var err error
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be an integer"))
			return
		}
	}
	page, err := s.app.AgentCategories(r.Context(), q.Get("state"), q.Get("after"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) runnerAgentCatalog(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	value, err := s.app.RunnerAgentCatalog(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
