package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
)

func (s *Server) maintenanceStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	v := protocol.MaintenanceOverview{}
	if s.cfg.MaintenanceStatus != nil {
		var err error
		v, err = s.cfg.MaintenanceStatus(r.Context())
		if err != nil {
			v = protocol.MaintenanceOverview{Configured: true, Error: "维护服务暂时不可达"}
		}
	}
	v.ObservedAt = s.cfg.Now().Unix()
	writeJSON(w, http.StatusOK, v)
}
