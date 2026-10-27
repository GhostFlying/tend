package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestOnlineRepositoryRunsAlongsideExistingTaskWithoutRestart(t *testing.T) {
	srv := newServer(t, time.Minute)
	gate := make(chan struct{})
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		script := passing(spec)
		var taskSpec domain.Spec
		if err := json.Unmarshal(spec.Spec, &taskSpec); err != nil {
			return testkit.WorkerScript{Err: err}
		}
		if spec.Role == protocol.RoleImplement && taskSpec.Workspace.Mounts[0].Repo == "tend" {
			script.Gate = gate
		}
		return script
	})
	bindings := gitws.New(gitws.Config{Home: t.TempDir()})
	n.repositories = bindings.RepositoryBindings
	n.heartbeat = 20 * time.Millisecond
	n.start()
	existing := srv.createTask()
	eventually(t, "the original worker starts", func() bool { return srv.task(existing).Lifecycle == protocol.TaskRunning })
	var initial store.HostRuntimeRow
	if err := srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		initial, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	payload, _ := json.Marshal(protocol.RegisterRepositoryPayload{RemoteURL: "https://example.com/new.git"})
	env := &protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "repos.register", Target: protocol.Target{Type: "repo", ID: "new"}, ExpectedControlRevision: &zero, Payload: payload}
	if op, err := srv.app.Execute(t.Context(), owner, env, app.RegisterRepository{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	if _, err := bindings.BindRepository(t.Context(), gitws.BindRepositoryRequest{RequestID: srv.ids.New(protocol.PrefixRequest), Binding: gitws.RepositoryBinding{Repo: "new", RemoteURL: "https://example.com/new.git"}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the same runner publishes its new binding", func() bool {
		matched := false
		_ = srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
			host, err := store.GetHost(t.Context(), tx.Q(), "linux-1")
			matched = err == nil && slices.Contains(host.Repos, "new")
			return err
		})
		return matched
	})
	created := srv.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "new repository", "spec": domain.Spec{Goal: "exercise dynamic repository", Acceptance: []string{"passes"}, Delivery: domain.DeliveryCommit, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "new"}}}}}, app.CreateTask{})
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(created.Result, &task); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new repository task completes", func() bool { return srv.task(task.TaskID).Lifecycle == protocol.TaskCompleted })
	if srv.task(existing).Lifecycle != protocol.TaskRunning {
		t.Fatal("registering a repository interrupted the original task")
	}
	if err := srv.store.Read(context.Background(), func(tx *store.ReadTx) error {
		current, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		if err != nil {
			return err
		}
		if current.Runtime.InstanceID != initial.Runtime.InstanceID || current.Runtime.Generation != initial.Runtime.Generation {
			t.Fatal("runner restarted during registration")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	eventually(t, "the original task completes", func() bool { return srv.task(existing).Lifecycle == protocol.TaskCompleted })
}

func TestSlowBindingPreservesExistingWorkerLease(t *testing.T) {
	srv := newServer(t, 800*time.Millisecond)
	workerGate := make(chan struct{})
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		script := passing(spec)
		script.Gate = workerGate
		return script
	})
	entered, unblock := make(chan struct{}), make(chan struct{})
	bindings := gitws.NewOrca(gitws.Config{Home: t.TempDir()}, &orca.Client{Exec: func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		close(entered)
		select {
		case <-unblock:
		case <-ctx.Done():
		}
		return nil, errors.New("simulated slow provider lookup")
	}})
	n.repositories = bindings.RepositoryBindings
	n.heartbeat = 30 * time.Millisecond
	n.start()
	srv.createTask()
	eventually(t, "existing worker starts", func() bool { return len(n.workers.Started()) > 0 })
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = bindings.BindRepository(t.Context(), gitws.BindRepositoryRequest{
			RequestID: "req_01ARZ3NDEKTSV4RRFFQ69G5FAV",
			Binding:   gitws.RepositoryBinding{Repo: "new", RemoteURL: "https://example.com/new.git", Orca: "id:new"},
		})
	}()
	defer func() { close(unblock); <-finished }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("binding did not start")
	}
	time.Sleep(1200 * time.Millisecond)
	if len(n.workers.Stopped()) > 0 {
		t.Fatal("binding an unrelated repo blocked heartbeat and fenced the active worker")
	}
}
