package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
)

func (s *Server) usageSources(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	value, err := s.app.UsageTargets(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (s *Server) usagePreview(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var input protocol.UsageUpload
	if err = decodeBodyLimit(w, r, &input, 16<<20); err != nil {
		s.writeError(w, r, err)
		return
	}
	value, err := s.app.PreviewUsage(r.Context(), p, input)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
