package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) managerRetirement(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	query := r.URL.Query()
	if query.Get("run_id") == "" {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "exact original run_id required"))
		return
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100"))
			return
		}
		limit = value
	}
	value, err := s.app.ManagerRetirement(r.Context(), r.PathValue("id"), query.Get("run_id"), query.Get("after"), limit)
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, value)
}
