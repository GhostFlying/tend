package traex

import (
	"github.com/GhostFlying/tend/internal/codexenv"
	"path/filepath"
	"testing"
)

func TestNativeMCPDiagnosticsDoNotInferConnectionFromCachedTools(t *testing.T) {
	for _, test := range []struct {
		runtime    string
		auth       string
		toolsError bool
		tools      int
		want       string
	}{
		{"", "unknown", false, 3, "catalog_loaded_connection_unverified"},
		{"failed", "bearerToken", false, 3, "connection_failed"},
		{"connected", "notLoggedIn", false, 3, "authentication_missing"},
		{"connected", "bearerToken", true, 0, "tool_loading_failed"},
		{"connected", "bearerToken", false, 0, "connected_no_tools"},
		{"connected", "bearerToken", false, 3, "connected_tools_loaded"},
		{"disabled", "unknown", false, 3, "disabled_by_policy"},
	} {
		if got := nativeMCPState(test.runtime, test.auth, test.toolsError, test.tools); got != test.want {
			t.Fatalf("runtime=%s auth=%s: got %s want %s", test.runtime, test.auth, got, test.want)
		}
	}
}

func TestManagedRolloutAliasesExposeOneFixedUsagePath(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "state")
	process := filepath.Join(runtime, ".tend-processes", "execution")
	s := &Session{environmentReference: &codexenv.Reference{Profile: codexenv.Profile{Home: runtime}, ProcessHome: process}}
	for _, name := range []string{"sessions", "archived_sessions"} {
		alias := filepath.Join(process, name, "date", "rollout.jsonl")
		fixed := filepath.Join(runtime, name, "date", "rollout.jsonl")
		if got := s.environmentRolloutPath(alias); got != fixed {
			t.Fatal("private alias escaped into usage identity", got)
		}
		if got := s.environmentRolloutPath(fixed); got != fixed {
			t.Fatal("canonical usage path changed", got)
		}
	}
	outside := filepath.Join(process, "sessions-other", "rollout.jsonl")
	if got := s.environmentRolloutPath(outside); got != outside {
		t.Fatal("unrelated native path was rewritten", got)
	}
}
