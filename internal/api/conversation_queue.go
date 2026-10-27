package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) conversationQueue(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	if category == "" {
		category = "owner"
	}
	after, err := intParam(r.URL.Query().Get("after"))
	if err != nil || (category != "owner" && category != "background" && category != "withdrawn") {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid queue category or cursor"))
		return
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		out, err := views.ConversationQueue(r.Context(), q, category, after, 50, r.URL.Query().Get("conversation_id"))
		out.Enabled = s.cfg.HostedMainAgent
		return out, err
	})
}
func (s *Server) conversationTranscript(w http.ResponseWriter, r *http.Request) {
	before, err := intParam(r.URL.Query().Get("before"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid transcript cursor"))
		return
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Transcript(r.Context(), q, before, 20, r.URL.Query().Get("conversation_id"))
	})
}
func (s *Server) conversationBlock(w http.ResponseWriter, r *http.Request) {
	after, err := intParam(r.URL.Query().Get("after"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid block cursor"))
		return
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.TranscriptBlock(r.Context(), q, r.PathValue("id"), after, 50)
	})
}
