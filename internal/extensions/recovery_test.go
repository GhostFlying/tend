package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSelectedSourceSubdirectoryCannotEscapeThroughLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires local link privilege")
	}
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inside"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"allowed": filepath.Join(root, "inside"), "escaped": outside} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sourceSubdirectory(root, "allowed"); err != nil {
		t.Fatal("internal source link rejected", err)
	}
	if _, err := sourceSubdirectory(root, "escaped"); err == nil {
		t.Fatal("external host directory accepted as Git provenance")
	}
	if _, err := sourceSubdirectory(root, "../"+filepath.Base(outside)); err == nil {
		t.Fatal("source traversal accepted")
	}
}

func TestRetainedBindingIgnoresNewWorkPrerequisitesAfterRestart(t *testing.T) {
	t.Setenv("TEND_TEST_RETAINED_MCP_TOKEN", "fixture")
	root := t.TempDir()
	base := filepath.Join(root, "base.toml")
	if err := os.WriteFile(base, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	c := Consumer{UserHome: t.TempDir(), Config: Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]Runtime{"native": {Home: filepath.Join(root, "home"), BaseConfig: base}}}, Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	agent := protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "worker"}
	res := protocol.ExtensionResource{Kind: "mcp", ID: "required", Version: "1", MCP: &protocol.ExtensionMCP{Transport: "http", URL: "https://example.test/mcp", BearerTokenEnvVar: "TEND_TEST_RETAINED_MCP_TOKEN", Required: true}}
	release, _, err := NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{res}, Categories: []protocol.ExtensionBinding{{CategoryID: "implementer", Choices: []protocol.ExtensionChoice{{Kind: "mcp", ID: "required", Version: "1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Receive(t.Context(), release, nil); err != nil {
		t.Fatal(err)
	}
	combo, err := Resolve(release, "implementer", agent)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := c.Prepare(t.Context(), combo.Pin, "native")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEND_TEST_RETAINED_MCP_TOKEN", "")
	if _, _, err := c.Prepare(t.Context(), combo.Pin, "native"); err == nil {
		t.Fatal("missing required local credential advertised new-work readiness")
	}
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	restarted := Agent{Consumer: &c}
	restored, err := restarted.Binding(t.Context(), combo.Pin, "native")
	if err != nil || restored == nil || *restored != *original {
		t.Fatal("historical receipt required new-work credentials or config", restored, err)
	}
	bad := combo.Pin
	bad.EffectiveSHA256 = Hash([]byte("changed"))
	if _, err := restarted.Binding(t.Context(), bad, "native"); err == nil {
		t.Fatal("restored a different composition")
	}
}

func TestHeartbeatAdoptionsAreBoundedAndHistoryRemainsPaged(t *testing.T) {
	root := t.TempDir()
	c := Consumer{UserHome: t.TempDir(), Config: Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]Runtime{"native": {Home: filepath.Join(root, "home")}}}, Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	release, _, err := NewRelease(protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{CategoryID: "implementer"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Receive(t.Context(), release, nil); err != nil {
		t.Fatal(err)
	}
	combo, _ := Resolve(release, "implementer", protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "worker"})
	binding, _, err := c.Prepare(t.Context(), combo.Pin, "native")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 90 {
		ref, err := binding.Pin(t.Context(), fmt.Sprintf("run:%03d", i))
		if err != nil {
			t.Fatal(err)
		}
		state := "ready"
		if i < 20 {
			state = "closed"
		}
		if err := ref.Record(t.Context(), state); err != nil {
			t.Fatal(err)
		}
	}
	status, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Adoptions) != 64 || !status.AdoptionsMore {
		t.Fatal("unbounded or silently incomplete telemetry", len(status.Adoptions), status.AdoptionsMore)
	}
	for _, row := range status.Adoptions {
		if row.State == "closed" {
			t.Fatal("historical closed run entered heartbeat")
		}
	}
	raw, _ := json.Marshal(status.Adoptions)
	if len(raw) > 128<<10 {
		t.Fatal("adoption byte budget exceeded", len(raw))
	}
	result, err := (&codexenv.Service{Root: binding.Directory}).Execute(t.Context(), codexenv.Request{Action: "show", ID: binding.ID, Limit: 10})
	if err != nil || len(result.Status.Adoptions) != 10 || !result.Status.AdoptionsMore || result.Status.Adoptions["run:000"].State != "closed" {
		t.Fatal("bounded telemetry erased historical diagnostics", err)
	}
}
