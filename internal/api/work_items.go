package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) workItems(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var page protocol.WorkItemPage
	err := s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		var err error
		page, err = views.WorkItems(r.Context(), tx.Q(), r.URL.Query().Get("project"), r.URL.Query().Get("include_closed") == "true", r.URL.Query().Get("column"))
		return err
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) workItem(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var revision int64
	if raw := r.URL.Query().Get("revision"); raw != "" {
		var err error
		revision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 1 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "revision must be a positive integer"))
			return
		}
	}
	var item protocol.WorkItemView
	err := s.store.Read(r.Context(), func(tx *store.ReadTx) error {
		var err error
		if revision != 0 {
			item.WorkItem, err = store.GetWorkItemRevision(r.Context(), tx.Q(), r.PathValue("id"), revision)
			item.Column = "historical"
			return err
		}
		item, err = views.WorkItem(r.Context(), tx.Q(), r.PathValue("id"))
		return err
	})
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, item)
}
