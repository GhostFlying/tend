package api

import (
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestContextIndexOwnerAPIAndQueryValidation(t *testing.T) {
	e := newEnv(t)
	path := "/api/v1/agent-conversations/main/context-index"
	var out protocol.ConversationContextIndex
	if code := e.do("GET", path, "", nil, &out); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	token := e.login(protocol.ScopeOwner)
	for _, section := range []string{"state", "sources", "facts", "threads"} {
		if code := e.do("GET", path+"?section="+section+"&limit=1", token, nil, &out); code != http.StatusOK || out.Section != section || out.Format != "index-v1" {
			t.Fatal(section, code, out)
		}
	}
	for _, query := range []string{"?section=bad", "?limit=51", "?limit=-1", "?limit=word"} {
		if code := e.do("GET", path+query, token, nil, nil); code != http.StatusBadRequest {
			t.Fatal(query, code)
		}
	}
}
