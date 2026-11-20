package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) runRollout(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		before, err := intParam(r.URL.Query().Get("before"))
		if err != nil || before < 0 {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid rollout cursor")
		}
		after, err := intParam(r.URL.Query().Get("after"))
		if err != nil || after < 0 || before > 0 && after > 0 {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid rollout cursor")
		}
		process := false
		if value := r.URL.Query().Get("process_text"); value != "" {
			process, err = strconv.ParseBool(value)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid process text filter")
			}
		}
		return views.RunRollout(r.Context(), q, r.PathValue("id"), before, after, process)
	})
}
