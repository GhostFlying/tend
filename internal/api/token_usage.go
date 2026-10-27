package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"net/http"
	"strconv"
)

func (s *Server) tokenUsage(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		params := r.URL.Query()
		kind := params.Get("window")
		if kind == "" {
			kind = "7d"
		}
		offset := 0
		if raw := params.Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n > 0 || n < -1200 {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid calendar offset")
			}
			offset = n
		}
		before, err := intParam(params.Get("before"))
		if err != nil || before < 0 {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid usage cursor")
		}
		return views.TokenUsage(r.Context(), q, kind, offset, params.Get("model"), params.Get("subject"), before, s.cfg.Now())
	})
}
