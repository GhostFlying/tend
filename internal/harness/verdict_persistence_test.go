package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAcknowledgedVerdictSurvivesWorkerAttempt(t *testing.T) {
	dir := t.TempDir()
	first := Verdicts{StateDir: dir}
	call := ToolCall{Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"passed","evidence":{"check":"owner-held verification"}}`)}
	if result := first.Handle(t.Context(), call); !result.Success {
		t.Fatal(result.Text)
	}
	if result := first.Handle(t.Context(), ToolCall{Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"invalid"}`)}); result.Success {
		t.Fatal("invalid verdict was acknowledged")
	}
	next := Verdicts{StateDir: dir}
	if err := next.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := next.Verdict(); got == nil || got.Verdict != protocol.VerdictPassed || string(got.Evidence) != `{"check":"owner-held verification"}` {
		t.Fatalf("lost acknowledged verdict: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "verdict.json"), []byte(`{"verdict":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := next.Restore(); err == nil {
		t.Fatal("corrupt history was treated as an absent verdict")
	}
}

func TestVerdictPersistenceFailureIsNotAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	v := Verdicts{StateDir: path}
	result := v.Handle(t.Context(), ToolCall{Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"passed","evidence":{}}`)})
	if result.Success || v.Verdict() != nil {
		t.Fatal("failed persistence produced a usable verdict")
	}
}
