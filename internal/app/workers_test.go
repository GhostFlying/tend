package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// withWorkers configures one host per worker kind, named after it.
func (h *harness) withWorkers(kinds ...protocol.WorkerKind) {
	h.t.Helper()
	c := testConfig()
	c.Hosts = nil
	for i, k := range kinds {
		c.Hosts = append(c.Hosts, HostConfig{HostID: string(k), OS: "linux", Arch: "amd64", Repos: []string{"tend", "douyin", "template-assembler"},
			Slots: 2, TokenHash: strings.Repeat(string(rune('a'+i)), 64), Nodes: []string{"tag:tend-runner"}, Worker: k})
	}
	if op := h.syncConfig(c); op.Status != protocol.OpSucceeded {
		h.t.Fatalf("config.sync: %+v", op)
	}
}

var multiSpec = domain.Spec{
	Goal: "link lynx", Acceptance: []string{"it builds"}, Delivery: domain.DeliveryCommit,
	Workspace: domain.Workspace{Template: "douyin-with-lynx"},
}

func (h *harness) queue() []protocol.QueueEntry {
	h.t.Helper()
	var q []protocol.QueueEntry
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		q, err = Queue(context.Background(), tx.Q(), scheduler.Order{Now: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return q
}

func TestConfigRefusesUnknownWorker(t *testing.T) {
	h := newHarness(t)
	c := testConfig()
	c.Hosts[0].Worker = "cove"
	if op := h.syncConfig(c); op.Status != protocol.OpFailed || !strings.Contains(op.Error.Message, "unknown worker") {
		t.Fatalf("config.sync = %+v, want unknown worker refused", op)
	}
}

func TestMultiMountRefusedWithoutSupportingHost(t *testing.T) {
	h := newHarness(t)
	h.withWorkers(protocol.WorkerOrca)
	op, _ := h.createTask(owner, createTaskPayload{Title: "a", Spec: multiSpec})
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest || op.Error.Message != MultiMountRefused {
		t.Fatalf("tasks.create = %+v, want refused", op)
	}
	if op, _ := h.createTask(owner, createTaskPayload{Title: "b", Spec: testSpec}); op.Status != protocol.OpSucceeded {
		t.Fatalf("single mount: %+v", op)
	}
}

func TestMultiMountAcceptedWithSupportingHost(t *testing.T) {
	h := newHarness(t)
	h.withWorkers(protocol.WorkerOrca, protocol.WorkerAppServer)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: multiSpec})
	if res.TaskID == "" {
		t.Fatal("multi-mount task refused while an app-server host is active")
	}
	if _, ok, _ := h.claim(string(protocol.WorkerOrca)); ok {
		t.Fatal("the orca host claimed a multi-mount task")
	}
	q := h.queue()
	if len(q) != 1 || q[0].Unplaceable != "" {
		t.Fatalf("queue = %+v, want the task placeable", q)
	}
	if _, ok, _ := h.claim(string(protocol.WorkerAppServer)); !ok {
		t.Fatal("the app-server host did not claim the task")
	}
}

func TestMultiMountTaskOutlivesItsHosts(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: multiSpec})
	h.withWorkers(protocol.WorkerOrca)
	q := h.queue()
	if len(q) != 1 || q[0].Unplaceable != scheduler.MultiMountUnsupported {
		t.Fatalf("queue = %+v, want the reason no host takes it", q)
	}
	// Its spec can still change; it keeps its mounts.
	changed := multiSpec
	changed.Goal = "link lynx and test it"
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &changed}, rev(res.ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("update_intent: %+v", op)
	}
}

func TestUpdateIntentRefusesNewMounts(t *testing.T) {
	h := newHarness(t)
	h.withWorkers(protocol.WorkerOrca)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	changed := multiSpec
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &changed}, rev(res.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpFailed || op.Error.Message != MultiMountRefused {
		t.Fatalf("update_intent = %+v, want refused", op)
	}
}

func TestUpdateIntentKeepsButDoesNotAddMounts(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: multiSpec})
	h.withWorkers(protocol.WorkerOrca)
	more := multiSpec
	more.Workspace = domain.Workspace{Mounts: []domain.Mount{{Repo: "douyin"}, {Repo: "template-assembler", Path: "lynx"}, {Repo: "tend"}}}
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &more}, rev(res.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpFailed || op.Error.Message != MultiMountRefused {
		t.Fatalf("adding a mount = %+v, want refused", op)
	}
	fewer := multiSpec
	fewer.Workspace = domain.Workspace{Mounts: []domain.Mount{{Repo: "douyin"}}}
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &fewer}, rev(res.ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("dropping a mount = %+v", op)
	}
}

func TestMultiMountAcceptedWithoutHosts(t *testing.T) {
	h := newHarness(t)
	c := testConfig()
	c.Hosts = nil
	h.syncConfig(c)
	if op, _ := h.createTask(owner, createTaskPayload{Title: "a", Spec: multiSpec}); op.Status != protocol.OpSucceeded {
		t.Fatalf("tasks.create = %+v, want accepted while no worker is known", op)
	}
	if q := h.queue(); len(q) != 1 || q[0].Unplaceable != "no host is active" {
		t.Fatalf("queue = %+v", q)
	}
}

// refused requires err to be an invalid_request that names the mismatch.
func refusedWorker(t *testing.T, what string, err error) {
	t.Helper()
	pe, ok := errors.AsType[*protocol.Error](err)
	if !ok || pe.Code != protocol.CodeInvalidRequest || !strings.Contains(pe.Message, "worker, but its runner runs") {
		t.Fatalf("%s: error = %v, want the worker mismatch refused", what, err)
	}
}

func TestFault_HostWorkerMismatch(t *testing.T) {
	h := newHarness(t)
	h.withWorkers(protocol.WorkerOrca)
	host := string(protocol.WorkerOrca)
	hello := func(w protocol.WorkerKind) error {
		_, err := h.app.Execute(context.Background(), runner(host), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: host}, protocol.HelloPayload{Worker: w}, nil), Hello{})
		return err
	}
	// A runner from before D64 runs the app-server.
	for _, w := range []protocol.WorkerKind{"", protocol.WorkerAppServer} {
		refusedWorker(t, "hello("+string(w)+")", hello(w))
	}
	if err := hello(protocol.WorkerOrca); err != nil {
		t.Fatalf("hello(orca) = %v", err)
	}
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	_, err := h.app.Execute(context.Background(), runner(host), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: host}, protocol.ClaimPayload{Worker: protocol.WorkerAppServer}, nil), Claim{})
	refusedWorker(t, "claim(appserver)", err)
	if as, ok, _ := h.claim(host); !ok || as.RunID == "" {
		t.Fatal("the orca runner claimed nothing")
	}
}

func TestFault_ClaimReplayedAfterWorkerChange(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	// The claim commits but its reply is lost; then the host moves to
	// orca while the app-server runner keeps running.
	_, ok, env := h.claim("linux-1")
	if !ok {
		t.Fatal("nothing claimed")
	}
	c := testConfig()
	c.Hosts[0].Worker = protocol.WorkerOrca
	h.syncConfig(c)
	_, err := h.app.Execute(context.Background(), runner("linux-1"), env, Claim{})
	refusedWorker(t, "replayed claim", err)
}
