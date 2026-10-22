//go:build integration

package e2e

import (
	"context"
	"net/http"
	"os"
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
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

// startHostedTraex uses the production thread configuration and the
// owner's shared traex home, with an isolated working directory and ledger.
func (s *server) startHostedTraex() {
	s.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	s.onStop(func() {
		cancel()
		wg.Wait()
	})
	cb := contextbuilder.New(s.app, s.store, s.ids, contextbuilder.Config{})
	front := mainthread.New(s.app, cb, tools.New(s.app, s.store, cb, quiet()), mainthread.Config{
		Instance: "main",
		Thread:   mainthread.ThreadConfig(s.t.TempDir(), traexModel()),
		Open: func(ctx context.Context) (harness.Session, error) {
			return traex.Start(ctx, traex.Options{Version: "it", Env: []string{protocol.AddonEnv + "=off"}})
		},
		Log: quiet(),
	})
	d := dispatch.New(s.app, map[string]dispatch.Handler{
		app.MainThreadDestination: front, app.MainThreadControlDestination: front.Control(),
	}, s.signal, time.Now, dispatch.Config{}, quiet())
	wg.Go(func() { _ = front.Run(ctx) })
	wg.Go(func() { _ = d.Run(ctx) })
}

// TestIntegration_HostedMainAgentReads runs the hosted main agent as
// tend-server does, in the traex home of the user running the test (D68),
// and asks it about a file outside its directory: the sandbox lets it read
// the file and the turn does not stop at an approval.
func TestIntegration_HostedMainAgentReads(t *testing.T) {
	srv := newServer(t, time.Minute)
	dir := t.TempDir()
	const word = "periwinkle-4127"
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("The code word is "+word+".\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv.startHostedTraex()

	srv.say("This is not about a task. Read the file " + filepath.Join(dir, "notes.txt") + " and tell me the code word in it.")
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if srv.count(`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent' AND body LIKE ?`, "%"+word+"%") > 0 {
			return
		}
		if srv.count(`SELECT COUNT(*) FROM main_turns WHERE status IN ('completed', 'failed', 'interrupted')`) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	var replies []string
	_ = srv.store.Read(context.Background(), func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(context.Background(), `SELECT body FROM conversation_messages WHERE author = 'main_agent'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b string
			_ = rows.Scan(&b)
			replies = append(replies, b)
		}
		return rows.Err()
	})
	t.Fatalf("the main agent did not report the code word; %d turns, %d ended; replies: %q",
		srv.count(`SELECT COUNT(*) FROM main_turns`), srv.count(`SELECT COUNT(*) FROM main_turns WHERE status != 'started'`), replies)
}

// TestIntegration_HostedWebTask checks the real main agent's dynamic
// tools and conversation through the API used by the web client. Workers
// are deterministic substitutes: this tests the hosted control path,
// not a model's implementation or acceptance of a repository change.
func TestIntegration_HostedWebTask(t *testing.T) {
	srv := newServer(t, time.Minute)
	srv.startHostedTraex()
	n := newNode(t, srv, passing)
	n.start()
	web := srv.webLogin(srv.startWeb().URL)
	events := web.stream("")
	asked := web.send(protocol.SubmitMessagePayload{Body: `Create exactly one task titled "document the widget" in the registered tend repository, using workspace name "web-integration". Its goal is to document the widget in README.md, its acceptance check is that README.md describes the widget, and its delivery is a commit. Dispatch it now without asking a question, then tell me its task ID. Do not implement it yourself.`})

	var taskID string
	var replied, createdTool bool
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	for taskID == "" || !replied || !createdTool {
		select {
		case ev, open := <-events.events:
			if !open {
				t.Fatal("the event stream ended before the task and reply completed")
			}
			if ev.EventSeq != events.last+1 {
				t.Fatalf("event %d follows %d", ev.EventSeq, events.last)
			}
			events.last = ev.EventSeq
			if ev.Kind == "task.completed" {
				taskID = ev.ObjectID
			}
			if ev.Item != nil && ev.Item.ReplyTo == asked && ev.Item.Tool != nil && ev.Item.Tool.Name == "tasks_create" && ev.Item.Tool.Status == protocol.ToolCompleted {
				createdTool = true
			}
			if itemIs(asked, protocol.MessageReplied)(ev) {
				replied = true
			}
		case <-deadline.C:
			t.Fatalf("hosted web flow timed out: completed task=%q, replied=%t, tasks_create completed=%t; conversation=%+v", taskID, replied, createdTool, web.conversation(""))
		}
	}

	var detail protocol.TaskDetail
	if st := web.do("GET", "/api/v1/tasks/"+taskID, nil, &detail); st != http.StatusOK {
		t.Fatalf("read the completed task = %d", st)
	}
	if detail.Lifecycle != protocol.TaskCompleted || len(detail.Acceptances) != 1 || detail.Acceptances[0].Verdict != protocol.VerdictPassed || len(detail.Artifacts) != 1 {
		t.Fatalf("task did not complete with independent acceptance: %+v", detail)
	}
	if srv.count(`SELECT COUNT(*) FROM tasks`) != 1 {
		t.Fatal("the main agent created more than one task")
	}
	var reply bool
	for _, item := range web.conversation("").Items {
		if item.Author == "main_agent" && item.ReplyTo == asked && strings.Contains(item.Body, taskID) {
			reply = true
		}
	}
	if !reply {
		t.Fatal("the stored main agent reply does not identify the task")
	}
	t.Logf("owner API -> real hosted main agent -> task %s -> substitute workers -> acceptance passed -> task.completed event", taskID)
}
