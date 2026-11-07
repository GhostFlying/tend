package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) issueMaterials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	query, err := protocol.ParseIssueMaterialsQuery(r.URL.Query())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	value, err := s.app.ReadIssueMaterials(r.Context(), r.PathValue("id"), query)
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, value)
}
