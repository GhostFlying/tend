package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) conversationImage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	image, err := s.app.ConversationImage(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, protocol.ConversationImageResponse{Image: image})
}
