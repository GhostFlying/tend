package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (s *Server) followups(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var page protocol.FollowupPage
	err := s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		if _, err := store.GetIssue(r.Context(), tx.Q(), r.PathValue("id")); err != nil {
			return err
		}
		var err error
		page, err = store.ListFollowups(r.Context(), tx.Q(), r.PathValue("id"), r.URL.Query().Get("after"))
		return err
	})
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	page.Enabled = s.cfg.HostedMainAgent
	writeJSON(w, http.StatusOK, page)
}
func (s *Server) followupHistory(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	after := int64(0)
	var err error
	if raw := r.URL.Query().Get("after_revision"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "invalid revision cursor"))
			return
		}
	}
	var page protocol.FollowupHistoryPage
	err = s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		var err error
		page, err = store.FollowupHistory(r.Context(), tx.Q(), r.PathValue("id"), r.URL.Query().Get("after_occurrence"), after)
		return err
	})
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, page)
}
