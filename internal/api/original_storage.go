package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) originalStorageStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	value, err := s.app.OriginalStorageStatus(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) originalStorageVerify(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 1000"))
			return
		}
	}
	value, err := s.app.VerifyOriginalStorage(r.Context(), r.URL.Query().Get("after"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
