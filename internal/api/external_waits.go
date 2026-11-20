package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) externalWaits(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		query := r.URL.Query()
		runID, status := query.Get("run_id"), query.Get("status")
		if query.Has("run_id") && (len(query["run_id"]) != 1 || runID == "" || strings.TrimSpace(runID) != runID) {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "run_id must be one nonempty original Run ID")
		}
		if query.Has("status") {
			if len(query["status"]) != 1 || !slices.Contains([]string{"stopping", "waiting", "continued", "cancelled"}, status) {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "status must be one of stopping, waiting, continued or cancelled")
			}
		}
		return store.TaskExternalWaits(r.Context(), q, r.PathValue("id"), runID, status)
	})
}

func (s *Server) ciContinuation(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.OwnerCIContinuation(r.Context(), q, r.PathValue("id"), r.PathValue("run"))
	})
}
