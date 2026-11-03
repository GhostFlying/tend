package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHandoffAuditOwnerReadAndValidation(t *testing.T) {
	e := newEnv(t)
	path := "/api/v1/conversation/handoff-audit?instance=main&after=0&limit=2"
	if code := e.do("GET", path, "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	token := e.login(protocol.ScopeOwner)
	var page protocol.HandoffAuditPage
	if code := e.do("GET", path, token, nil, &page); code != http.StatusOK || page.Instance != "main" || page.Items == nil || page.HistoryComplete {
		t.Fatal(code, page)
	}
	for _, suffix := range []string{"?instance=main&after=bad", "?instance=main&limit=bad", "?instance=main&limit=201", "?after=0"} {
		if code := e.do("GET", "/api/v1/conversation/handoff-audit"+suffix, token, nil, nil); code != http.StatusBadRequest {
			t.Fatal(suffix, code)
		}
	}
}
