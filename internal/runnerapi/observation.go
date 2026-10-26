package runnerapi

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
)

func (s *Server) observation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	if r.URL.RawQuery != "" {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidRequest, "observation takes no query parameters; scope comes from the task spec"))
		return
	}
	result, err := s.app.ReadRunObservation(r.Context(), p)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
