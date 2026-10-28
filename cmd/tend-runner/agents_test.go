package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestAgentCLIUsesExistingControl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix local control")
	}
	for _, long := range []bool{false, true} {
		name := "short"
		if long {
			name = "long"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if long {
				home = filepath.Join(home, strings.Repeat("long-home-", 16))
			}
			if err := os.MkdirAll(filepath.Join(home, "runner"), 0700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(home, "definitions.json")
			if err := os.WriteFile(source, []byte(`{"format_version":1,"runtimes":[],"agents":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			bootstrap := config{Home: home, HostID: "runner-a", Agents: &agentcontrol.Config{SourceFile: source}}
			clock := testkit.NewClock()
			ids := testkit.NewIDGen(clock, 18)
			agents, err := assembleAgentControl(t.Context(), home, bootstrap.HostID, bootstrap.Agents, clock.Now, ids)
			if err != nil {
				t.Fatal(err)
			}
			defer agents.Close()
			stop, err := startRepositoryControl(t.Context(), home, gitws.New(gitws.Config{Home: home}), agents.Handler())
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			data, err := json.Marshal(bootstrap)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "runner.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			var accepted protocol.LocalAgentConfigurationReceipt
			for _, operation := range []string{"status", "validate", "reload", "status"} {
				var out, errout bytes.Buffer
				if code := runAgentsCommand([]string{operation, "--config", path}, &out, &errout); code != 0 {
					t.Fatalf("%s exit=%d %s %s", operation, code, out.String(), errout.String())
				}
				var result protocol.LocalAgentConfigurationResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Current == nil {
					t.Fatal(out.String(), err)
				}
				if operation == "reload" {
					if result.Receipt == nil || result.Current.Revision != 1 {
						t.Fatal(result)
					}
					accepted = *result.Receipt
				}
				if operation == "validate" && (result.Candidate == nil || result.Current.Revision != 0) {
					t.Fatal("validate committed source", result)
				}
			}
			var out, errout bytes.Buffer
			if code := runAgentsCommand([]string{"reload", "--config", path, "--request-id", accepted.RequestID, "--revision", "0"}, &out, &errout); code != 0 {
				t.Fatal("retry failed", code, out.String(), errout.String())
			}
			var replay protocol.LocalAgentConfigurationResult
			_ = json.Unmarshal(out.Bytes(), &replay)
			if replay.Receipt == nil || *replay.Receipt != accepted || replay.Current.Revision != 1 {
				t.Fatal("CLI retry changed accepted snapshot", replay)
			}
			// Both command families use the original listener and locator.
			socket, err := repositorySocketPath(home)
			if err != nil {
				t.Fatal(err)
			}
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			response, err := client.Get("http://localhost/repositories")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatal("existing repository route changed", response.StatusCode)
			}
		})
	}
}

func TestAgentControlDisabledLeavesLegacyDefault(t *testing.T) {
	home := t.TempDir()
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 19)
	control, err := assembleAgentControl(t.Context(), home, "runner-a", nil, clock.Now, ids)
	if err != nil || control != nil {
		t.Fatal("disabled option created controller", control, err)
	}
	if _, err := os.Stat(filepath.Join(home, "runner", "agents")); !os.IsNotExist(err) {
		t.Fatal("disabled option created registry", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	path := filepath.Join(home, "runner.json")
	if err := os.WriteFile(path, []byte(`{"host_id":"runner-a","home":"unused"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := runAgentsCommand([]string{"reload", "--config", path}, &out, &errout); code != 1 || !strings.Contains(errout.String(), "not enabled") {
		t.Fatal(code, out.String(), errout.String())
	}
}
