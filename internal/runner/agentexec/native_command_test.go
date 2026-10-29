package agentexec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
)

func TestRuntimeExecutableSelectsActualProbeAndFactoryCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fake CLI")
	}
	directory := t.TempDir()
	command := filepath.Join(directory, "native-cli")
	calls := filepath.Join(directory, "calls")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quote(calls) + "\nprintf '%s\\n' '{\"ok\":true,\"result\":{}}'\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	input := BuildInput{Runtime: agentconfig.RuntimeBinding{Kind: agentconfig.OrcaWorker, CLI: "codex", ExecutableRef: command}, Agent: agentconfig.AgentDefinition{Model: "requested-model"}, Installation: agentregistry.InstallationReference{LocalReference: "PROVENANCE_ONLY_NOT_AN_EXECUTABLE"}}
	pool := &Pool{}
	if err := pool.probe(t.Context(), input); err != nil {
		t.Fatal("probe used provenance instead of the requested executable", err)
	}
	workers, err := pool.build(input)
	if err != nil {
		t.Fatal(err)
	}
	// The existing factory's real read-only release command also uses ExecutableRef.
	if err := workers.(harness.Releaser).Release(context.Background(), "run", `{"worktree":"original-provider"}`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"folder-workspace list", "project-group list", "terminal list"} {
		if !strings.Contains(string(raw), operation) {
			t.Fatal("native command not observed", operation, string(raw))
		}
	}
	// A usable provenance locator cannot rescue an unavailable requested command.
	input.Runtime.ExecutableRef = filepath.Join(directory, "missing-native-command")
	input.Installation.LocalReference = command
	if err := pool.probe(t.Context(), input); err == nil {
		t.Fatal("missing requested executable silently used provenance")
	}
}
