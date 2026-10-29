package upgradectl

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestArchiveLocalCLIUsesPersistentGateHostCASAndExplicitCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local Unix control")
	}
	// Keep Unix socket short independently of the test name.
	dir, err := os.MkdirTemp("", "tend-archive-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "control.sock")
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 778)
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "active.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := app.New(st, ids, clock.Now)
	runOp := func(p protocol.Principal, operation string, target protocol.Target, value any, command app.Command) {
		t.Helper()
		raw, _ := json.Marshal(value)
		op, err := a.Execute(t.Context(), p, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw}, command)
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
	}
	runOp(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Hosts: []app.HostConfig{{HostID: "runner-a", OS: "linux", Arch: "amd64", Slots: 1, Nodes: []string{"runner-node"}, TokenHash: strings.Repeat("a", 64)}}}, app.SyncConfig{})
	controller := ArchiveControl{App: a, ObserveHosted: func(context.Context) (bool, error) { return true, nil }}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- ServeLocal(ctx, socket, controller.Handler()) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("local socket did not start")
		case <-time.After(time.Millisecond):
		}
	}
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(""), IDs: ids, Getenv: func(string) string { return "" }}
	command := func(args ...string) int {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		return cli.Main(t.Context(), env, append([]string{"archive", args[0], "--socket", socket, "--json"}, args[1:]...))
	}
	request := ids.New(protocol.PrefixRequest)
	if code := command("prepare", "--request-id", request, "--revision", "0"); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if code := command("host", "--host", "runner-a"); code == 0 {
		t.Fatal("unsupported host reported maintained")
	}
	p, _ := a.ArchivePreparation(t.Context())
	if !p.Prepared {
		t.Fatal("host partial failure reopened gate")
	}
	rt := &protocol.RunnerRuntime{InstanceID: "current", Generation: 1, Sequence: 1, Protocol: 1, ProtocolMin: 0, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance}}
	runner := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "runner-a"}
	runOp(runner, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "runner-a"}, protocol.HelloPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: rt}, app.Hello{})
	if code := command("host", "--host", "runner-a"); code != 0 {
		t.Fatal(code, stderr.String())
	}
	rt.Sequence++
	rt.Mode = protocol.RunnerReady
	rt.AppliedControlRevision = 1
	if _, err := a.RuntimeHeartbeat(t.Context(), runner, protocol.HeartbeatPayload{Runtime: rt}); err != nil {
		t.Fatal(err)
	}
	if code := command("status"); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var view protocol.ArchiveOnlineStatus
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil || !view.OnlineReady {
		t.Fatal(stdout.String(), err)
	}
	if code := command("cancel"); code != 0 {
		t.Fatal(code, stderr.String())
	}
	p, _ = a.ArchivePreparation(t.Context())
	if p.Prepared {
		t.Fatal("cancel did not reopen server")
	}
	if code := command("prepare", "--request-id", request, "--revision", "0"); code != 0 {
		t.Fatal("original receipt not replayed", stderr.String())
	}
	p, _ = a.ArchivePreparation(t.Context())
	if p.Prepared {
		t.Fatal("replay reapplied old control")
	}
	view, err = controller.Status(t.Context())
	if err != nil || view.Hosts[0].Control.Mode != protocol.RunnerMaintenance {
		t.Fatal("cancel silently restored host", view, err)
	}
	if code := command("prepare", "--request-id", request); code != 2 {
		t.Fatal("retry identity silently chose a new revision", code, stderr.String())
	}
	// Explicitly restore only the chosen host; no automatic bulk resume.
	if code := command("host", "--host", "runner-a", "--mode", "normal"); code != 0 {
		t.Fatal(code, stderr.String())
	}
	// A normal process restart loads the stored preparation without opening it.
	if code := command("prepare"); code != 0 {
		t.Fatal(code, stderr.String())
	}
	restarted := app.New(st, ids, clock.Now)
	if _, err := restarted.BeginOwnerMutation(t.Context()); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal(err)
	}
}
