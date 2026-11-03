package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) handoffAudit(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	after, limit := int64(0), 50
	var err error
	if raw := r.URL.Query().Get("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
	}
	if raw := r.URL.Query().Get("limit"); raw != "" && err == nil {
		limit, err = strconv.Atoi(raw)
	}
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid audit cursor or limit"))
		return
	}
	page, err := s.app.HandoffAudit(r.Context(), r.URL.Query().Get("instance"), after, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
