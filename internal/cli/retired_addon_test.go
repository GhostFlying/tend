package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredAddonCommandsNeverContactServer(t *testing.T) {
	f := newFakeServer(t)
	for _, args := range [][]string{{"mcp", "--agent", "traex"}, {"hook", "codex", "session-start"}, {"setup", "claude", "--write"}} {
		t.Run(args[0], func(t *testing.T) {
			e := newTestEnv(t, f.srv.URL)
			if code := e.main(args...); code != exitUsage {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(e.errOut.String(), "unknown command") {
				t.Fatal(e.errOut.String())
			}
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatal("retired command contacted server")
	}
}

func TestLoginRemovesRetiredCredentials(t *testing.T) {
	f := newFakeServer(t)
	f.answer("POST /api/v1/session", map[string]any{"deployment_id": "new-deployment", "token": "new-owner-token", "scope": "owner"})
	e := newTestEnv(t, f.srv.URL)
	path := filepath.Join(e.ConfigDir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"client_id":"cli-0011223344556677","owner_token":"old","agent_token":"retired","deployment_id":"old-deployment"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := e.main("login"); code != 0 {
		t.Fatal(e.errOut.String())
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(b), "agent_token") || strings.Contains(string(b), "old-deployment") {
		t.Fatalf("credentials not replaced: %v", err)
	}
	reqs := f.seen("POST /api/v1/session")
	if len(reqs) != 1 || !strings.Contains(string(reqs[0].body), `"scope":"owner"`) {
		t.Fatal("login requested non-owner credentials")
	}
	if _, err = e.client("agent"); err == nil {
		t.Fatal("old scope selected owner credentials")
	}
}
