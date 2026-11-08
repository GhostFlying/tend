package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityCodexEnvironmentInputsAndDryRun(t *testing.T) {
	e := newTestEnv(t, "")
	var received protocol.LocalCodexEnvironmentRequest
	calls := 0
	e.CodexEnvironment = func(_ context.Context, q protocol.LocalCodexEnvironmentRequest) (json.RawMessage, error) {
		received = q
		calls++
		return json.RawMessage(`{"changed":true}`), nil
	}
	if code := e.main("codex-env", "plugin", "add", "sample", "--item", "fixture@local", "--source", "/fixture/package", "--request-id", "original", "--revision", "3"); code != exitOK {
		t.Fatal(e.errOut)
	}
	if received.Action != "plugin add" || received.ID != "sample" || received.ExpectedRevision != 3 || received.RequestID != "original" || received.ItemID != "fixture@local" {
		t.Fatal(received)
	}
	if code := e.main("codex-env", "delete", "sample", "--dry-run"); code != exitOK || calls != 1 {
		t.Fatal("dry run performed mutation", e.errOut)
	}
	if code := e.main("codex-env", "show", "sample", "--root", "relative"); code != exitUsage {
		t.Fatal("relative state root accepted")
	}
}
