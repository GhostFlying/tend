package api

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"testing"
)

func TestTokenUsageAndRolloutReadsRequireOwnerAndValidateWindows(t *testing.T) {
	e := newEnv(t)
	owner := e.login(protocol.ScopeOwner)
	agent := e.retiredAgentToken(clientID)
	for _, token := range []string{"", agent} {
		for _, path := range []string{"/api/v1/token-usage", "/api/v1/runs/missing/rollout"} {
			status := e.do("GET", path, token, nil, nil)
			if status != 401 && status != 403 {
				t.Fatalf("%s: %d", path, status)
			}
		}
	}
	var v protocol.TokenUsageView
	if status := e.do("GET", "/api/v1/token-usage?window=total", owner, nil, &v); status != http.StatusOK || v.Totals.TotalKnown != 0 || v.Timezone != "Asia/Shanghai" {
		t.Fatal(status, v)
	}
	for _, query := range []string{"window=invalid", "window=day&offset=1", "before=-1"} {
		if status := e.do("GET", "/api/v1/token-usage?"+query, owner, nil, nil); status != 400 {
			t.Fatal(query, status)
		}
	}
	if status := e.do("GET", "/api/v1/runs/missing/rollout", owner, nil, nil); status != 404 {
		t.Fatal(status)
	}
}
