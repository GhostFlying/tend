package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type deferredReportWorkspace struct{ *workspaces }

func (w deferredReportWorkspace) Prepare(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	p, err := w.workspaces.Prepare(ctx, as)
	p.Report.Path = p.Dir
	return p, err
}
func (deferredReportWorkspace) Cleanup(context.Context, protocol.WorkspaceCleanup) error { return nil }

func startDeferredResourceNode(n *node) {
	n.t.Helper()
	j := n.journal()
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel, n.done = cancel, make(chan error, 1)
	r := runner.New(runner.Config{ServerURL: n.srv.url, HostID: "linux-1", HostToken: hostToken, Version: "test", HTTPClient: &http.Client{Transport: n.net}, Journal: j, Worker: protocol.WorkerOrca, Workers: n.workers, Workspaces: deferredReportWorkspace{n.ws}, Now: time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: quiet(), RetryDelay: 20 * time.Millisecond, HeartbeatInterval: 20 * time.Millisecond, EventFlush: n.flush})
	go func() { err := r.Run(ctx); j.Close(); n.done <- err }()
}

func setResourceMaintenance(t *testing.T, s *server, mode string) {
	t.Helper()
	ctx := t.Context()
	var control protocol.RunnerControl
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		control, err = store.GetHostControl(ctx, tx.Q(), "linux-1")
		return err
	}); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"mode": mode})
	env := protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: "hosts.maintenance", Target: protocol.Target{Type: "host", ID: "linux-1"}, ExpectedControlRevision: &control.Revision, Payload: payload}
	op, err := s.app.Execute(ctx, owner, &env, app.HostMaintenance{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("maintenance: %+v %v", op, err)
	}
}

func resourceOwner(t *testing.T, s *server, task string) string {
	t.Helper()
	var owner string
	if err := s.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COALESCE(occupied_by,'') FROM workspaces WHERE task_id=?`, task).Scan(&owner)
	}); err != nil {
		t.Fatal(err)
	}
	return owner
}

// An implementation may finish while maintenance prevents the same workspace's
// acceptance from starting. A confirmed deferred release must not deadlock the
// runner replacement that makes that acceptance runnable again.
func TestFault_MaintenanceBetweenImplementationAndAcceptance(t *testing.T) {
	for _, finish := range []string{"accept after restart", "cancel without another run"} {
		t.Run(finish, func(t *testing.T) {
			srv := newServer(t, time.Minute)
			gate := make(chan struct{})
			n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
				if spec.Role == protocol.RoleAccept {
					return passing(spec)
				}
				return testkit.WorkerScript{Gate: gate, Result: harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "investigation complete"}}}}
			})
			startDeferredResourceNode(n)
			eventually(t, "runner registers workspace capability", func() bool {
				var err error
				_ = srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
					_, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
					return nil
				})
				return err == nil
			})
			op := srv.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "investigate", "spec": domain.Spec{Goal: "investigate", Acceptance: []string{"independently verify report"}, Deliverables: []string{domain.DeliveryReport}, Workspace: domain.Workspace{Mode: "temporary"}}}, app.CreateTask{})
			var created struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(op.Result, &created); err != nil {
				t.Fatal(err)
			}
			task := created.TaskID
			eventually(t, "implementation starts", func() bool { return len(n.workers.Started()) == 1 })
			setResourceMaintenance(t, srv, protocol.RunnerMaintenance)
			eventually(t, "runner acknowledges maintenance", func() bool {
				var rt store.HostRuntimeRow
				_ = srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					rt, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
					return err
				})
				return rt.Runtime.Mode == protocol.RunnerMaintenance
			})
			close(gate)
			eventually(t, "implementation finishes with acceptance queued", func() bool { return srv.task(task).Lifecycle == protocol.TaskAwaitingAcceptance })
			eventually(t, "runner is ready despite the retained task workspace", func() bool {
				var rt store.HostRuntimeRow
				_ = srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					rt, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
					return err
				})
				return rt.Runtime.Mode == protocol.RunnerReady && rt.Runtime.PendingOperations == 0 && rt.Runtime.PendingClaims == 0
			})
			if resourceOwner(t, srv, task) != task {
				t.Fatal("deferred release changed real ownership")
			}
			if len(n.workers.Started()) != 1 {
				t.Fatal("maintenance started queued acceptance")
			}
			n.stop()
			if finish == "cancel without another run" {
				tr := srv.task(task)
				payload := json.RawMessage(`{}`)
				env := protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "tasks.cancel", Target: protocol.Target{Type: "task", ID: task}, ExpectedControlRevision: &tr.ControlRevision, Payload: payload}
				op, err := srv.app.Execute(t.Context(), owner, &env, app.CancelTask{})
				if err != nil || op.Status != protocol.OpSucceeded {
					t.Fatalf("cancel: %+v %v", op, err)
				}
			} else {
				setResourceMaintenance(t, srv, protocol.RunnerNormal)
			}
			startDeferredResourceNode(n)
			if finish == "accept after restart" {
				eventually(t, "acceptance completes after replacement", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
			}
			eventually(t, "terminal scan recovers and releases retained workspace", func() bool { return resourceOwner(t, srv, task) == "" })
			want := 1
			if finish == "accept after restart" {
				want = 2
			}
			if len(n.workers.Started()) != want {
				t.Fatalf("unexpected worker replay: %d", len(n.workers.Started()))
			}
		})
	}
}
