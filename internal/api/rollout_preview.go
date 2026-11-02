package api

import (
	"encoding/json"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) rolloutPreview(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	query, err := protocol.ParseRolloutPreviewQuery(r.URL.Query())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.app.ReadRolloutPreview(r.Context(), p, r.PathValue("id"), query)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
