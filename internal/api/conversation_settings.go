package api

import (
	"context"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) settings(ctx context.Context) protocol.ConversationSettingsView {
	view := protocol.ConversationSettingsView{Enabled: s.cfg.HostedMainAgent, Models: []protocol.ConversationModel{}}
	if !view.Enabled || s.cfg.ConversationSettings == nil {
		return view
	}
	current, err := s.cfg.ConversationSettings(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "could not read hosted conversation settings", "err", err)
		return view
	}
	current.Enabled = true
	return current
}

func (s *Server) conversationSettings(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settings(r.Context()))
}
