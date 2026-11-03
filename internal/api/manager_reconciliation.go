package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) managerRunReconciliation(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	run := r.URL.Query().Get("run_id")
	if run == "" {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "exact original run_id required"))
		return
	}
	value, err := s.app.ManagerRunReconciliation(r.Context(), r.PathValue("id"), run)
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, value)
}
