package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"strconv"
)

func (s *Server) feishuBinding(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.app.FeishuBinding(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) feishuInputs(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	after, limit := int64(0), 100
	var err error
	if v := r.URL.Query().Get("after"); v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid after cursor"))
			return
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid limit"))
			return
		}
	}
	page, err := s.app.FeishuInputs(r.Context(), after, limit, r.URL.Query().Get("status"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
