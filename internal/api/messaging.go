package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) messaging(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	kind := r.PathValue("view")
	var result any
	var err error
	switch {
	case r.PathValue("channel") != "":
		channels, e := s.app.MessagingChannels(r.Context())
		err = e
		for _, c := range channels {
			if c.ID == r.PathValue("channel") {
				result = c
			}
		}
		if err == nil && result == nil {
			err = protocol.Errorf(protocol.CodeNotFound, "channel not found")
		}
	case r.URL.Path == "/api/v1/messaging/channels":
		result, err = s.app.MessagingChannels(r.Context())
	case id == "":
		after, e := intParam(r.URL.Query().Get("after"))
		if e != nil || after < 0 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "after must be a nonnegative cursor"))
			return
		}
		result, err = s.app.MessagingMessages(r.Context(), after, r.URL.Query().Get("conversation_id"), r.URL.Query().Get("issue_id"), r.URL.Query().Get("channel_id"))
	case kind == "receipts":
		result, err = s.app.MessagingReceipts(r.Context(), id)
	case kind == "trace":
		result, err = s.app.MessagingTrace(r.Context(), id)
	case kind == "":
		result, err = s.app.MessagingMessage(r.Context(), id)
	default:
		err = protocol.Errorf(protocol.CodeNotFound, "unknown communication view")
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
