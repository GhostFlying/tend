//go:build linux

package agentexec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
)

const nativeOwnerParentHelper = "TEND_TEST_NATIVE_OWNER_PARENT"
const nativeOwnerServerHelper = "TEND_TEST_NATIVE_OWNER_SERVER"
const nativeOwnerProbeDirectory = "TEND_TEST_NATIVE_OWNER_PROBE_DIR"

// The app-server deliberately survives stdin EOF. Its exit after the runner
// crashes must come from process containment, not from cooperative pipe cleanup.
func runNativeOwnerAppServerProbe(t *testing.T) {
	dir := os.Getenv(nativeOwnerProbeDirectory)
	if err := os.WriteFile(filepath.Join(dir, "native.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "native.args"), []byte(strings.Join(os.Args, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				time.Sleep(20 * time.Second)
				return
			}
			t.Fatal(err)
		}
		if request.Method == "hooks/list" {
			t.Fatal("native owner was changed back into a restricted hosted process")
		}
		if len(request.ID) != 0 {
			if err := encoder.Encode(map[string]any{"id": request.ID, "result": map[string]any{}}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func runNativeOwnerRunnerParentProbe(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv(nativeOwnerProbeDirectory)
	command := filepath.Join(dir, "native-cli")
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
	script := "#!/bin/sh\n" + nativeOwnerServerHelper + "=1 exec " + quoted + " -test.run=^TestNativeOwnerProcessDiesWithRunnerCrash$ -test.timeout=25s -- \"$@\"\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f := newSource(t)
	f.config.FormatVersion = 2
	f.config.Runtimes[0].CLI = "codex"
	f.config.Runtimes[0].ExecutableRef = command
	f.config.Runtimes[0].DeclaredPurposes = []agentconfig.Purpose{agentconfig.IssueManager}
	f.config.Agents[0].Purposes = []agentconfig.Purpose{agentconfig.IssueManager}
	f.reload(t)
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), ProbeOwner: func(context.Context, agentexec.BuildInput) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	pin := observed(t, p).Pin
	manager, err := p.ResolveManager(t.Context(), pin)
	if err != nil {
		t.Fatal(err)
	}
	if manager.OpenNative == nil {
		t.Fatal("native manager entry is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session, err := manager.OpenNative(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("opened-native-owner"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Second)
}

func nativeOwnerProcessState(pid int) (start string, running bool, err error) {
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false, err
	}
	index := strings.LastIndex(string(body), ") ")
	if index < 0 {
		return "", false, errors.New("native process stat is malformed")
	}
	fields := strings.Fields(string(body)[index+2:])
	if len(fields) < 20 {
		return "", false, errors.New("native process stat is incomplete")
	}
	return fields[19], fields[0] != "Z" && fields[0] != "X", nil
}

func TestNativeOwnerProcessDiesWithRunnerCrash(t *testing.T) {
	if os.Getenv(nativeOwnerServerHelper) == "1" {
		runNativeOwnerAppServerProbe(t)
		return
	}
	if os.Getenv(nativeOwnerParentHelper) == "1" {
		runNativeOwnerRunnerParentProbe(t)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	parent := exec.CommandContext(t.Context(), executable, "-test.run=^TestNativeOwnerProcessDiesWithRunnerCrash$", "-test.timeout=25s")
	parent.Env = append(os.Environ(), nativeOwnerParentHelper+"=1", nativeOwnerProbeDirectory+"="+dir)
	log, err := os.Create(filepath.Join(dir, "parent.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	parent.Stdout, parent.Stderr = log, log
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = parent.Process.Kill()
		_ = parent.Wait()
	}()
	var pid int
	var nativeStart string
	defer func() {
		if pid > 1 {
			current, running, err := nativeOwnerProcessState(pid)
			if err == nil && running && current == nativeStart {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if body, err := os.ReadFile(filepath.Join(dir, "native.pid")); err == nil {
			pid, err = strconv.Atoi(string(body))
			if err != nil {
				t.Fatal(err)
			}
			if nativeStart == "" {
				nativeStart, _, _ = nativeOwnerProcessState(pid)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case <-deadline.C:
			body, _ := os.ReadFile(filepath.Join(dir, "parent.log"))
			t.Fatalf("runner did not open the ordinary native owner: %s", body)
		case <-tick.C:
		}
	}
	if pid <= 1 {
		t.Fatal("native owner did not report its actual process identity")
	}
	start, running, err := nativeOwnerProcessState(pid)
	if err != nil || !running {
		t.Fatalf("native owner was not running before the runner crash: %t %v", running, err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "native.args"))
	if err != nil || strings.Contains(string(args), "hooks.PreToolUse") || !strings.HasSuffix(string(args), "app-server") {
		t.Fatalf("native owner tool configuration was replaced: %s %v", args, err)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := parent.Wait(); err == nil {
		t.Fatal("runner fixture did not crash")
	}
	deadline.Reset(2 * time.Second)
	for {
		current, running, err := nativeOwnerProcessState(pid)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) || err == nil && (current != start || !running) {
			pid = 0
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatal("native owner process survived the runner crash without lease supervision")
		case <-tick.C:
		}
	}
}
