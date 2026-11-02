package agentexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/harness/traex"
)

func TestLocalRuntimesDoNotRequireCentralWorkers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix CLI wrapper")
	}
	for _, kind := range []agentconfig.RuntimeKind{agentconfig.AppServerWorker, agentconfig.OrcaWorker} {
		t.Run(string(kind), func(t *testing.T) {
			directory := t.TempDir()
			command, calls := filepath.Join(directory, "native-cli"), filepath.Join(directory, "calls")
			t.Setenv("TEND_TEST_LOCAL_NATIVE_CLI", calls)
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
			script := "#!/bin/sh\nexec " + quote(self) + " -test.run=^TestLocalNativeCLIProcess$ -- \"$@\"\n"
			if err := os.WriteFile(command, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			centralProbes := 0
			pool := &Pool{options: Options{ProbeCentral: func(context.Context) error {
				centralProbes++
				return errors.New("central workers disabled")
			}}}
			input := BuildInput{Runtime: agentconfig.RuntimeBinding{Kind: kind, CLI: "codex", ExecutableRef: command}}
			if err := pool.probe(t.Context(), input); err != nil {
				t.Fatal("local readiness now depends on central configuration", err)
			}
			workers, err := pool.build(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, central := workers.(interface{ NativeLiveMessages() }); central {
				t.Fatal("local message delivery was replaced by central steering")
			}
			switch kind {
			case agentconfig.AppServerWorker:
				if _, ok := workers.(*traex.Workers); !ok {
					t.Fatalf("local app-server factory changed: %T", workers)
				}
			case agentconfig.OrcaWorker:
				if _, ok := workers.(*orca.Workers); !ok {
					t.Fatalf("local Orca factory changed: %T", workers)
				}
				if err := workers.(harness.Releaser).Release(t.Context(), "run", `{"worktree":"original-provider"}`); err != nil {
					t.Fatal("local Orca cleanup requires central transport", err)
				}
			}
			data, err := os.ReadFile(calls)
			if err != nil || len(data) == 0 || centralProbes != 0 || strings.Contains(string(data), "exec-server") {
				t.Fatalf("unexpected native path: calls=%q centralProbes=%d error=%v", data, centralProbes, err)
			}
		})
	}
}

// TestLocalNativeCLIProcess is a subprocess-only, model-free protocol fixture.
func TestLocalNativeCLIProcess(t *testing.T) {
	path := os.Getenv("TEND_TEST_LOCAL_NATIVE_CLI")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(1)
	}
	_, err = fmt.Fprintln(file, strings.Join(os.Args, " "))
	file.Close()
	if err != nil {
		os.Exit(1)
	}
	if os.Args[len(os.Args)-1] != "app-server" {
		fmt.Println(`{"ok":true,"result":{}}`)
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(1)
		}
		if len(request.ID) > 0 {
			if err := encoder.Encode(map[string]any{"id": request.ID, "result": map[string]any{}}); err != nil {
				os.Exit(1)
			}
		}
	}
	if scanner.Err() != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
