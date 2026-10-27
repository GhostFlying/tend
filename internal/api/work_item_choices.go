package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (s *Server) workItemChoice(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var c protocol.WorkItemChoice
	err := s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		var err error
		c, err = store.GetWorkItemChoice(r.Context(), tx.Q(), r.PathValue("id"))
		return err
	})
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, c)
}
