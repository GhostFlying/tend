package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (s *Server) backgroundSources(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid source cursor"))
			return
		}
	}
	var p protocol.BackgroundSourcePage
	err := s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		var err error
		p, err = store.BackgroundSources(r.Context(), tx.Q(), r.PathValue("id"), after)
		return err
	})
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}
func (s *Server) coordinationMetrics(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var p protocol.CoordinationMetrics
	err := s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		var err error
		p, err = store.ReadCoordinationMetrics(r.Context(), tx.Q())
		return err
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
