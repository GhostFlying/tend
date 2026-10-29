package api

import (
	"errors"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (s *Server) hostedAgents(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.app.HostedAgentInventory(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, protocol.HostedAgentInventory{
			State: "unconfigured",
			Projection: protocol.AgentInventoryProjection{
				FormatVersion: 1,
				SourceKind:    protocol.AgentSourceInline,
				Runtimes:      []protocol.AgentInventoryRuntime{},
				Agents:        []protocol.AgentInventoryDefinition{},
			},
		})
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
