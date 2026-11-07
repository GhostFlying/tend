package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) issueDeploymentPreparation(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if r.URL.RawQuery != "" {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "deployment preparation accepts only the Issue ID"))
		return
	}
	value, err := s.app.ReadIssueDeploymentPreparation(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, value)
}
