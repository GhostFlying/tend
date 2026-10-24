package e2e

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestEndToEnd_CompletionNoticeWakesMainAndPersistsAcrossRestart(t *testing.T) {
	srv := newServer(t, time.Minute)
	agent := testkit.NewHarness()
	type observed struct{ thread, packet string }
	injected := make(chan observed, 4)
	agent.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		packets := agent.Injected(th.ID())
		injected <- observed{th.ID(), packets[len(packets)-1]}
		if strings.Contains(in.Text, "create completion check") {
			th.CallTool(context.Background(), turn, "create", "tasks_create", map[string]any{
				"title": "notice task", "commit_seq": 1, "workspace_name": "notice-task",
				"spec": domain.Spec{Goal: "notice task", Acceptance: []string{"works"}, Delivery: domain.DeliveryCommit,
					Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}},
			})
		}
		th.Say(turn, "reply", "The ledger is the source of truth.")
		return "completed"
	}
	srv.startMainThread(agent)
	web := srv.webLogin(srv.startWeb().URL)
	stream := web.stream("")
	n := newNode(t, srv, passing)
	n.start()
	web.send(protocol.SubmitMessagePayload{Body: "create completion check"})
	events := stream.until("system completion notice", func(e protocol.LedgerEvent) bool {
		return e.Item != nil && e.Item.Author == "system"
	})
	noticeEvent := events[len(events)-1]
	notice := *noticeEvent.Item
	if notice.Kind != protocol.ItemMessage || notice.ReplyTo != "" ||
		!strings.Contains(notice.Body, commitSHA) || !strings.Contains(notice.Body, "spec 1") {
		t.Fatalf("notice = %+v", notice)
	}
	eventually(t, "owner and automatic completion turn settle", func() bool {
		return srv.count("SELECT COUNT(*) FROM main_turns WHERE status='completed'") == 2
	})
	starts := agent.Starts()
	if len(starts) != 2 || starts[1].UserMessageID != notice.MessageID || !strings.Contains(starts[1].Text, "Tend ledger event") || !strings.Contains(starts[1].Text, "not an owner request") {
		t.Fatalf("completion did not initiate exactly one system event turn: %+v", starts)
	}
	var completed protocol.TaskPage
	if st := web.do("GET", "/api/v1/tasks?all=1&lifecycle=completed", nil, &completed); st != http.StatusOK || len(completed.Tasks) != 1 {
		t.Fatalf("completed tasks = %d %+v", st, completed)
	}
	taskID := completed.Tasks[0].TaskID
	if !strings.Contains(notice.Body, taskID) {
		t.Fatal("notice does not identify its completed task")
	}
	assertNotice := func(c *webClient) {
		t.Helper()
		var found []protocol.ConversationItem
		for _, item := range c.conversation("").Items {
			if item.Author == "system" {
				found = append(found, item)
			}
		}
		if len(found) != 1 || found[0].MessageID != notice.MessageID || found[0].Seq != notice.Seq ||
			found[0].Body != notice.Body || !found[0].CreatedAt.Equal(notice.CreatedAt) {
			t.Fatalf("persisted notice changed: %+v", found)
		}
	}
	assertNotice(web)
	first := <-injected
	completion := <-injected
	if !strings.Contains(completion.packet, "task.completed") || !strings.Contains(completion.packet, taskID) {
		t.Fatalf("automatic turn missed completion facts: %s", completion.packet)
	}
	var replies int
	for _, item := range web.conversation("").Items {
		if item.Author == "main_agent" && item.ReplyTo == notice.MessageID {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("background replies bound to completion notice = %d, want 1", replies)
	}
	web.send(protocol.SubmitMessagePayload{Body: "read completion facts"})
	eventually(t, "next owner turn settles", func() bool {
		return srv.count("SELECT COUNT(*) FROM main_turns WHERE status='completed'") == 3
	})
	<-injected // The completion facts were already confirmed by the background turn.
	n.stop()
	srv = srv.restart()
	srv.startMainThread(agent)
	reloaded := srv.webLogin(srv.startWeb().URL)
	assertNotice(reloaded)
	replay := reloaded.stream("?after=" + strconv.FormatInt(noticeEvent.EventSeq-1, 10))
	replayed := replay.until("same completion notice after restart", func(e protocol.LedgerEvent) bool {
		return e.Item != nil && e.Item.MessageID == notice.MessageID
	})
	if replayed[len(replayed)-1].Item.Body != notice.Body || len(agent.Starts()) != 3 {
		t.Fatal("restart changed the notice or replayed an automatic turn")
	}
	cb := contextbuilder.New(srv.app, srv.store, srv.ids, contextbuilder.Config{})
	if err := cb.Invalidate(t.Context(), "main/"+first.thread); err != nil {
		t.Fatal(err)
	}
	reloaded.send(protocol.SubmitMessagePayload{Body: "read rebuilt history"})
	eventually(t, "rebuilt-context turn settles", func() bool {
		return srv.count("SELECT COUNT(*) FROM main_turns WHERE status='completed'") == 4
	})
	full := <-injected
	if !strings.Contains(full.packet, "system") || !strings.Contains(full.packet, notice.Body) {
		t.Fatalf("rebuilt context missed the system notice: %s", full.packet)
	}
	assertNotice(reloaded)
}
