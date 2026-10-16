//go:build integration

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

// TestIntegration_TraexLoop runs the whole loop with real traex: the owner
// asks in plain words, the main agent plans a task, a runner implements it
// in a git workspace and an independent run accepts it.
func TestIntegration_TraexLoop(t *testing.T) {
	env := gitIdentity(t)
	url := calcRemote(t, env)
	srv := newServer(t, time.Minute)
	srv.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "calc", RemoteURL: url}},
		Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Repos: []string{"calc"}, Slots: 2,
			TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:tend-runner"}}},
	}, app.SyncConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	cb := contextbuilder.New(srv.app, srv.store, srv.ids, contextbuilder.Config{})
	front := mainthread.New(srv.app, cb, tools.New(srv.app, srv.store, cb, quiet()), mainthread.Config{
		Instance: "main",
		Thread: harness.ThreadConfig{Cwd: t.TempDir(), Model: traexModel(), DeveloperInstructions: mainthread.Instructions,
			Sandbox: "danger-full-access", ApprovalPolicy: "never", Config: mainthread.Isolation},
		Open: func(ctx context.Context) (harness.Session, error) {
			return traex.Start(ctx, traex.Options{Version: "it"})
		},
		Log: quiet(),
	})
	d := dispatch.New(srv.app, map[string]dispatch.Handler{app.MainThreadDestination: front}, srv.signal, time.Now, dispatch.Config{}, quiet())
	wg.Go(func() { _ = front.Run(ctx) })
	wg.Go(func() { _ = d.Run(ctx) })

	home := t.TempDir()
	j, err := journal.Open(ctx, filepath.Join(home, "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	r := runner.New(runner.Config{
		ServerURL: srv.url, HostID: "linux-1", HostToken: hostToken, Version: "it", HTTPClient: http.DefaultClient, Journal: j,
		Workers:    traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Version: "it"}, Model: traexModel()}),
		Workspaces: gitws.New(gitws.Config{Home: home, Env: env}),
		Now:        time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: quiet(), RetryDelay: time.Second,
	})
	wg.Go(func() { _ = r.Run(ctx) })

	srv.say("In the calc repository, please add a function Sub(a, b int) int that returns a - b, with a unit test. " +
		"Accept it when go test ./... passes.")
	deadline := time.Now().Add(40 * time.Minute)
	for time.Now().Before(deadline) {
		if srv.count(`SELECT COUNT(*) FROM tasks WHERE lifecycle = 'completed'`) == 1 {
			t.Logf("completed: %d runs, %d agent messages", srv.count(`SELECT COUNT(*) FROM runs`),
				srv.count(`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent'`))
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("the task did not complete in time:\n%s", srv.describe())
}

// describe summarizes the ledger for a failing test.
func (s *server) describe() string {
	var b strings.Builder
	_ = s.store.Read(context.Background(), func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(context.Background(), `SELECT task_id, title, lifecycle, block_reason FROM tasks`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, title, lifecycle, block string
			_ = rows.Scan(&id, &title, &lifecycle, &block)
			fmt.Fprintf(&b, "task %s %q %s block=%s\n", id, title, lifecycle, block)
		}
		runs, err := tx.Q().QueryContext(context.Background(), `SELECT run_id, role, observed_state FROM runs ORDER BY created_at`)
		if err != nil {
			return err
		}
		defer runs.Close()
		for runs.Next() {
			var id, role, state string
			_ = runs.Scan(&id, &role, &state)
			fmt.Fprintf(&b, "run %s %s %s\n", id, role, state)
		}
		return nil
	})
	return b.String()
}
