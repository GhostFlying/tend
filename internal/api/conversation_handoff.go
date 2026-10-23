package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) conversationHandoff(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	view := protocol.ConversationHandoffView{Enabled: s.cfg.HostedMainAgent}
	if view.Enabled && s.cfg.ConversationHandoff != nil {
		current, err := s.cfg.ConversationHandoff(r.Context())
		if err != nil {
			s.log.ErrorContext(r.Context(), "could not read conversation handoff", "err", err)
			s.writeError(w, r, protocol.Errorf(protocol.CodeInternal, "could not read conversation handoff"))
			return
		}
		view = current
		view.Enabled = true
	}
	writeJSON(w, http.StatusOK, view)
}
