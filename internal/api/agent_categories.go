package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"net/http"
)

func (s *Server) agentCategory(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return store.GetAgentCategory(r.Context(), q, r.PathValue("id"))
	})
}
