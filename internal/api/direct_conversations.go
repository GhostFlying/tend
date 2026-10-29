package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) manualSource(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.app.ManualSource(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) runMessages(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	after, err := intParam(r.URL.Query().Get("after"))
	if err != nil {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid after cursor"))
		return
	}
	v, err := s.app.RunMessages(r.Context(), p, r.PathValue("id"), after, 100, false)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) nativeEntry(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.app.NativeEntry(r.Context(), p, r.PathValue("id"), r.URL.Query().Get("provider"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
