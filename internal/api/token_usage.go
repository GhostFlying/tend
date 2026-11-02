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
		if group := params.Get("group_by"); group != "" && group != "session" && group != "issue" {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid usage grouping")
		}
		if order := params.Get("sort"); order != "" && order != "recent" && order != "total" {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid usage order")
		}
		v, err := views.TokenUsageFiltered(r.Context(), q, kind, offset, params.Get("model"), params.Get("subject"), before, s.cfg.Now(), views.UsageFilter{Agent: params.Get("agent"), Role: params.Get("role"), Issue: params.Get("issue"), Session: params.Get("session"), Sort: params.Get("sort")})
		if err != nil {
			return nil, err
		}
		if params.Get("group_by") != "" && params.Get("details") != "1" {
			v.Records = []protocol.TokenUsageRecord{}
			v.Trend = []protocol.TokenUsageBucket{}
			for i := range v.Sessions {
				v.Sessions[i].Executions = []protocol.UsageExecution{}
			}
			for i := range v.Issues {
				v.Issues[i].Sessions = []protocol.UsageSession{}
			}
		}
		return v, nil
	})
}
