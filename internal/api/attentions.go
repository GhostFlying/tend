package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"net/http"
	"strconv"
)

func (s *Server) attentions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := views.AttentionFilter{IssueID: q.Get("issue_id"), Kind: q.Get("kind"), Status: q.Get("status"), After: q.Get("after"), IncludeArchived: q.Get("include_archived") == "true", Limit: 50}
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100"))
			return
		}
		f.Limit = n
	}
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) { return views.Attentions(r.Context(), q, f) })
}
func (s *Server) attention(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return views.Attention(r.Context(), q, r.PathValue("id"))
	})
}
