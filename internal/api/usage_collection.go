package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (s *Server) usageCollections(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		p := r.URL.Query()
		limit := 0
		if p.Has("limit") {
			var err error
			limit, err = strconv.Atoi(p.Get("limit"))
			if err != nil || limit < 1 || limit > 100 {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be 1..100")
			}
		}
		page, err := app.UsageCollections(r.Context(), q, protocol.UsageCollectionQuery{Host: p.Get("host"), Run: p.Get("run"), Conversation: p.Get("conversation"), Thread: p.Get("thread"), State: p.Get("state"), Reason: p.Get("reason"), After: p.Get("after"), Limit: limit}, r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		if r.PathValue("id") != "" {
			return page.Sources[0], nil
		}
		return page, nil
	})
}
