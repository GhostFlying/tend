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
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
)

// TestIntegration_TraexLoop runs the whole loop with real traex: the owner
// asks through the web client's API, the main agent plans a task, a runner
// implements it in a git workspace, an independent run accepts it and the
// client observes completion over its event stream.
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
	srv.startHostedTraex()

	home := t.TempDir()
	j, err := journal.Open(ctx, filepath.Join(home, "journal.db"), time.Now)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	srv.onStop(func() {
		cancel()
		wg.Wait()
		j.Close()
	})
	r := runner.New(runner.Config{
		ServerURL: srv.url, HostID: "linux-1", HostToken: hostToken, Version: "it", HTTPClient: http.DefaultClient, Journal: j,
		Workers:    traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Version: "it"}, Model: traexModel()}),
		Workspaces: gitws.New(gitws.Config{Home: home, Env: env}),
		Now:        time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: quiet(), RetryDelay: time.Second,
	})
	wg.Go(func() { _ = r.Run(ctx) })

	web := srv.webLogin(srv.startWeb().URL)
	events := web.stream("")
	web.send(protocol.SubmitMessagePayload{Body: "In the calc repository, please add a function Sub(a, b int) int that returns a - b, with a unit test. " +
		"Accept it when go test ./... passes. Deliver it as a commit in workspace add-sub; do not push."})
	deadline := time.NewTimer(40 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case ev, open := <-events.events:
			if !open {
				t.Fatal("the owner's event stream ended before task completion")
			}
			if ev.EventSeq != events.last+1 {
				t.Fatalf("event %d follows %d", ev.EventSeq, events.last)
			}
			events.last = ev.EventSeq
			if ev.Kind != "task.completed" {
				continue
			}
			var detail protocol.TaskDetail
			if st := web.do("GET", "/api/v1/tasks/"+ev.ObjectID, nil, &detail); st != http.StatusOK {
				t.Fatalf("read the completed task = %d", st)
			}
			if detail.Lifecycle != protocol.TaskCompleted || len(detail.Acceptances) == 0 || len(detail.Artifacts) == 0 {
				t.Fatalf("task.completed has no accepted artifact: %+v", detail)
			}
			t.Logf("owner API and SSE completed: %d real runs, %d agent messages", len(detail.Runs),
				srv.count(`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent'`))
			return
		case <-deadline.C:
			t.Fatalf("the task did not complete in time:\n%s", srv.describe())
		}
	}
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
