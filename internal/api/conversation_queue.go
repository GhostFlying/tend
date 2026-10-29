package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) conversationQueue(w http.ResponseWriter, r *http.Request) {
	if !s.validConversationProjection(w, r) {
		return
	}
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
		if err != nil {
			return out, err
		}
		return views.OwnerIssueQueue(r.Context(), q, out)
	})
}
func (s *Server) conversationTranscript(w http.ResponseWriter, r *http.Request) {
	if !s.validConversationProjection(w, r) {
		return
	}
	before, err := intParam(r.URL.Query().Get("before"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid transcript cursor"))
		return
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		out, err := views.Transcript(r.Context(), q, before, 20, r.URL.Query().Get("conversation_id"))
		if err != nil {
			return out, err
		}
		return views.OwnerIssueTranscript(r.Context(), q, out)
	})
}
func (s *Server) conversationBlock(w http.ResponseWriter, r *http.Request) {
	if !s.validConversationProjection(w, r) {
		return
	}
	after, err := intParam(r.URL.Query().Get("after"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid block cursor"))
		return
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		out, err := views.TranscriptBlock(r.Context(), q, r.PathValue("id"), after, 50)
		if err != nil {
			return out, err
		}
		return views.OwnerIssueTranscriptBlock(r.Context(), q, out)
	})
}

func (s *Server) validConversationProjection(w http.ResponseWriter, r *http.Request) bool {
	projection := r.URL.Query().Get("projection")
	if projection != "" && projection != "issues" {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "unknown conversation projection"))
		return false
	}
	return true
}

func (s *Server) ownerMessage(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.OwnerIssueMessage(r.Context(), q, r.PathValue("id"))
	})
}
