package traex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const envProbeServer = "TEND_TRAEX_TEST_ENV_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(envProbeServer) == "1" {
		if err := serveEnvProbe(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

type envProbe struct {
	OrcaKeys []string          `json:"orca_keys"`
	Settings map[string]string `json:"settings"`
}

// serveEnvProbe reexecutes this test binary as a minimal app-server.
func serveEnvProbe() error {
	probe := envProbe{Settings: map[string]string{}}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "ORCA_") {
			probe.OrcaKeys = append(probe.OrcaKeys, key)
		}
	}
	for _, key := range []string{"TRAE_HOME", "TEND_TEST_NORMAL", "TEND_TEST_AUTH"} {
		probe.Settings[key] = os.Getenv(key)
	}
	raw, err := json.Marshal(probe)
	if err != nil {
		return err
	}
	if err := os.WriteFile(os.Getenv("TEND_TEST_ENV_FILE"), raw, 0o600); err != nil {
		return err
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request map[string]json.RawMessage
		if err := decoder.Decode(&request); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if id, ok := request["id"]; ok {
			if err := encoder.Encode(map[string]any{"id": id, "result": map[string]any{}}); err != nil {
				return err
			}
		}
	}
}

func TestStartIsolatesOrcaContext(t *testing.T) {
	keys := []string{
		"ORCA_PANE_KEY", "ORCA_AGENT_PANE", "ORCA_TERMINAL_HANDLE", "ORCA_TAB_ID",
		"ORCA_WORKTREE_ID", "ORCA_WORKSPACE_ID", "ORCA_AGENT_LAUNCH_TOKEN",
		"ORCA_AGENT_LAUNCH", "ORCA_AGENT_SESSION_ID", "ORCA_AGENT_HOOK_ENDPOINT",
		"ORCA_AGENT_HOOK_TOKEN", "ORCA_FUTURE_ALIAS", "orca_mixed_case",
	}
	for _, key := range keys {
		t.Setenv(key, "parent-pane")
	}
	t.Setenv("TRAE_HOME", t.TempDir())
	t.Setenv("TEND_TEST_NORMAL", "inherited")
	t.Setenv("TEND_TEST_AUTH", "synthetic-auth")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "environment.json")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, Options{Bin: bin, Env: []string{
		envProbeServer + "=1", "TEND_TEST_ENV_FILE=" + file, "TEND_TEST_NORMAL=override",
		"ORCA_PANE_KEY=override-pane", "ORCA_AGENT_PANE=override-pane", "orca_override=alias",
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var probe envProbe
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.OrcaKeys) != 0 {
		t.Errorf("child inherited Orca context: %v", probe.OrcaKeys)
	}
	want := map[string]string{"TRAE_HOME": os.Getenv("TRAE_HOME"), "TEND_TEST_NORMAL": "override", "TEND_TEST_AUTH": "synthetic-auth"}
	for key, value := range want {
		if probe.Settings[key] != value {
			t.Errorf("child %s = %q, want %q", key, probe.Settings[key], value)
		}
	}
	for _, key := range keys {
		if os.Getenv(key) != "parent-pane" {
			t.Errorf("parent %s changed", key)
		}
	}
}

func TestAppServerEnvPreservesInputs(t *testing.T) {
	inherited := []string{"ORCA_PANE_KEY=parent", "PATH=bin", "NORMAL=old"}
	overrides := []string{"ORCA_AGENT_PANE=parent", "NORMAL=new=value", "ORCAS=ordinary"}
	before, extra := slices.Clone(inherited), slices.Clone(overrides)
	want := []string{"PATH=bin", "NORMAL=old", "NORMAL=new=value", "ORCAS=ordinary"}
	if got := appServerEnv(inherited, overrides); !slices.Equal(got, want) {
		t.Errorf("environment = %v, want %v", got, want)
	}
	if !slices.Equal(inherited, before) || !slices.Equal(overrides, extra) {
		t.Fatal("caller environment slices changed")
	}
}
