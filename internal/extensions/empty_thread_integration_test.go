//go:build linux && integration

package extensions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIntegrationEmptyThreadAdoptsWithoutModelInput(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("native Codex unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	c := Consumer{UserHome: user, Config: Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]Runtime{"native": {Home: filepath.Join(root, "home")}}}}
	identity := protocol.AgentIdentity{ExecutionEnvironmentID: "fixture", LocalID: "agent"}
	first, _, err := NewRelease(protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{CategoryID: "planner", AgentsMD: "FIRST"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Receive(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	combo, _ := Resolve(first, "planner", identity)
	binding, _, err := c.Prepare(ctx, combo.Pin, "native")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	opts := traex.Options{Bin: "codex", Dir: cwd, Environment: binding, EnvironmentExecution: "coordinator:empty"}
	session, err := traex.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	config := harness.ThreadConfig{Cwd: cwd, Model: "fixture", Sandbox: "read-only", ApprovalPolicy: "never"}
	thread, err := session.StartThread(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	next := first.Catalog
	next.Categories = []protocol.ExtensionBinding{{CategoryID: "planner", AgentsMD: "SECOND"}}
	release, _, err := NewRelease(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Receive(ctx, release, nil); err != nil {
		t.Fatal(err)
	}
	nextCombo, _ := Resolve(release, "planner", identity)
	target, _, err := c.Prepare(ctx, nextCombo.Pin, "native")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.QueueAdoption(ctx, "coordinator:empty"); err != nil {
		t.Fatal(err)
	}
	if ready, err := session.EnvironmentQuiescent(ctx, thread.ID()); err != nil || !ready {
		t.Fatal("empty native thread cannot reach adoption boundary", ready, err)
	}
	if err := session.AdoptEnvironment(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	opts.Environment = target
	resumed, err := traex.Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	restored, err := resumed.ResumeThread(ctx, thread.ID(), config)
	if err != nil || restored.ID() != thread.ID() || resumed.EnvironmentPin() != nextCombo.Pin {
		t.Fatal("empty thread identity changed during adoption", err)
	}
	t.Log("empty native thread persisted and resumed under the next composition without any model turn")
}
