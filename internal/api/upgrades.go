package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
)

func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	row, err := s.app.GetUpgrade(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeNotFound, "upgrade not found"))
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Upgrade protocol.UpgradeView `json:"upgrade"`
	}{row.View})
}
