//go:build integration

package e2e

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/hostedconfig"
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
	makeManager := func(conversation string) *mainthread.Manager {
		dir := s.t.TempDir()
		return mainthread.New(s.app, cb, tools.New(s.app, s.store, cb, quiet()), mainthread.Config{
			Instance: "integration/" + conversation, ConversationID: conversation, Thread: mainthread.ThreadConfig(dir, "unused"),
			OpenNamed: func(ctx context.Context, selection hostedconfig.Selection) (harness.Session, error) {
				return traex.Start(ctx, traex.Options{Bin: selection.Runtime.ExecutableRef, Dir: dir, Version: "it", Env: []string{protocol.AddonEnv + "=off"}, HostedCoordination: true})
			}, Log: quiet()})
	}
	front := makeManager("")
	pool := &mainthread.BackgroundPool{App: s.app, Front: front, New: func(id string) (*mainthread.Manager, error) { return makeManager(id), nil }}
	wg.Go(func() { pool.Run(ctx) })

	d := dispatch.New(s.app, map[string]dispatch.Handler{
		app.MainThreadDestination: front, app.MainThreadControlDestination: pool,
	}, s.signal, time.Now, dispatch.Config{}, quiet())
	wg.Go(func() { _ = front.Run(ctx) })
	wg.Go(func() { _ = d.Run(ctx) })
}

// TestIntegration_HostedMainAgentReadsOwnerMaterial uses bounded owner-provided
// material through Tend; the planner must not independently read native files.
func TestIntegration_HostedMainAgentReadsOwnerMaterial(t *testing.T) {
	srv := newNamedFixtureServer(t, traexModel(), "traex")
	const word = "periwinkle-4127"

	srv.startHostedTraex()

	srv.say("Owner-provided bounded note: The code word is " + word + ". This is ordinary discussion; repeat the code word using only this message, without native file or research tools.")
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
	srv := newNamedFixtureServer(t, traexModel(), "traex")
	srv.startHostedTraex()
	n := newNode(t, srv, passing)
	n.start()
	web := srv.webLogin(srv.startWeb().URL)
	events := web.stream("")
	asked := web.send(protocol.SubmitMessagePayload{Body: `Delegate to the registered Issue manager exactly one task titled "document the widget" in the registered tend repository, using workspace name "web-integration". Its goal is to document the widget in README.md, its acceptance check is that README.md describes the widget, and its delivery is a commit. Dispatch it now without asking a question, then tell me the delegated Issue ID. Do not implement it yourself.`})

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
			if ev.Item != nil && ev.Item.Tool != nil && ev.Item.Tool.Name == "issues_create_task" && ev.Item.Tool.Status == protocol.ToolCompleted {
				createdTool = true
			}
			if itemIs(asked, protocol.MessageReplied)(ev) {
				replied = true
			}
		case <-deadline.C:
			t.Fatalf("hosted web flow timed out: completed task=%q, replied=%t, issues_create_task completed=%t; conversation=%+v", taskID, replied, createdTool, web.conversation(""))
		}
	}

	var detail protocol.TaskDetail
	if st := web.do("GET", "/api/v1/tasks/"+taskID, nil, &detail); st != http.StatusOK {
		t.Fatalf("read the completed task = %d", st)
	}
	if detail.PrimaryIssueID == "" || detail.Lifecycle != protocol.TaskCompleted || len(detail.Acceptances) != 1 || detail.Acceptances[0].Verdict != protocol.VerdictPassed || len(detail.Artifacts) != 1 {
		t.Fatalf("task did not complete with independent acceptance: %+v", detail)
	}
	if srv.count(`SELECT COUNT(*) FROM tasks`) != 1 {
		t.Fatal("the main agent created more than one task")
	}
	var reply bool
	for _, item := range web.conversation("").Items {
		if item.Author == "main_agent" && item.ReplyTo == asked && strings.Contains(item.Body, detail.PrimaryIssueID) {
			reply = true
		}
	}
	if !reply {
		t.Fatal("the stored main agent reply does not identify its delegated Issue")
	}
	t.Logf("owner API -> real named main -> Issue manager -> task %s -> substitute workers -> acceptance passed -> task.completed event", taskID)
}
