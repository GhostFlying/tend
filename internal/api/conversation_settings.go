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
	view := s.settings(r.Context())
	if id := r.URL.Query().Get("conversation_id"); id != "" && id != "main" {
		c, err := s.app.AgentConversation(r.Context(), id)
		if err != nil {
			s.writeError(w, r, notFound(err))
			return
		}
		view.Model, view.Effort, view.Revision = c.Model, c.Effort, c.Revision
	}
	writeJSON(w, http.StatusOK, view)
}
