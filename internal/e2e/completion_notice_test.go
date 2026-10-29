package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestHistoricalEndToEnd_CompletionNoticeWakesMainAndPersistsAcrossRestart(t *testing.T) {
	srv := newServer(t, time.Minute)
	agent := testkit.NewHarness()
	failures := make(chan string, 8)
	agent.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {

		th.Say(turn, "reply", "The ledger is the source of truth.")
		return "completed"
	}
	background := func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		item := strings.TrimPrefix(th.Config.Cwd, "/background/work-item:")
		sources := th.CallTool(context.Background(), turn, "sources", "background_sources", map[string]any{})
		if !sources.Success {
			failures <- sources.Text
			return "failed"
		}
		result := th.CallTool(context.Background(), turn, "context", "background_item_read", map[string]any{"work_item_id": item})
		var contextView struct {
			OK     bool                  `json:"ok"`
			Result protocol.WorkItemView `json:"result"`
		}
		if !result.Success || json.Unmarshal([]byte(result.Text), &contextView) != nil || !contextView.OK || contextView.Result.WorkItemID != item {
			failures <- result.Text
			return "failed"
		}
		// Assessment uses the frozen context actually injected into this turn,
		// even if main has since prepared a newer historical baseline.
		delivered := historicalDeliveredContextRevision(t, agent, th.ID())

		th.Say(turn, "review", "Completion evidence retained; coordinator review required.")
		result = th.CallTool(context.Background(), turn, "assessment", "background_submit", protocol.BackgroundAssessmentPayload{ContextRevision: delivered, Disposition: "escalate", Note: "Completion requires coordinator review", EvidenceIDs: []string{}})
		if !result.Success {
			failures <- result.Text
			return "failed"
		}
		return "completed"
	}
	srv.startHistoricalMainThread(agent, background)
	web := srv.webLogin(srv.startWeb().URL)
	stream := web.stream("")
	// Explicit historical owner setup; the active main tool registry cannot
	// create standalone legacy tasks. The original notice/restart transport is retained.
	srv.createTask()
	n := newNode(t, srv, passing)
	n.start()
	web.send(protocol.SubmitMessagePayload{Body: "Observe the historical task completion"})
	events := stream.until("system completion notice", func(e protocol.LedgerEvent) bool {
		return e.Item != nil && e.Item.Author == "system" && strings.Contains(e.Item.Body, commitSHA)
	})
	ev := events[len(events)-1]
	notice := *ev.Item
	if notice.Kind != protocol.ItemMessage || notice.ReplyTo != "" || !strings.Contains(notice.Body, "spec 1") {
		t.Fatal(notice)
	}
	eventually(t, "structured background escalation and main review settle", func() bool {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		default:
		}
		return srv.count(`SELECT COUNT(*) FROM background_assessments WHERE disposition='escalate'`) == 1 &&
			srv.count(`SELECT COUNT(*) FROM logical_turns WHERE status<>'completed'`) == 0 &&
			srv.count(`SELECT COUNT(*) FROM outbox WHERE (destination='main_thread' OR destination LIKE 'background_thread:%') AND status IN ('pending','sending')`) == 0 &&
			srv.count(`SELECT COUNT(*) FROM conversation_messages WHERE body='Completion evidence retained; coordinator review required.'`) == 1
	})
	var tasks protocol.TaskPage
	if st := web.do("GET", "/api/v1/tasks?all=1&lifecycle=completed", nil, &tasks); st != http.StatusOK || len(tasks.Tasks) != 1 {
		t.Fatal(st, tasks)
	}
	if !strings.Contains(notice.Body, tasks.Tasks[0].TaskID) {
		t.Fatal("notice lost task identity")
	}
	var batch, conversation string
	if err := srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT b.message_id,m.conversation_id FROM background_batches b JOIN conversation_messages m ON m.message_id=b.message_id LIMIT 1`).Scan(&batch, &conversation)
	}); err != nil {
		t.Fatal(err)
	}
	if conversation == "main" {
		t.Fatal("background batch stayed in main")
	}
	var page protocol.ConversationPage
	if st := web.do("GET", "/api/v1/conversation?conversation_id="+conversation, nil, &page); st != http.StatusOK {
		t.Fatal(st)
	}
	var sawReply bool
	for _, m := range page.Items {
		if m.ReplyTo == batch && m.Author == "main_agent" {
			sawReply = true
		}
	}
	if !sawReply {
		t.Fatal("background rollout reply missing from its conversation")
	}
	assertNotice := func(c *webClient) {
		t.Helper()
		count := 0
		for _, item := range c.conversation("").Items {
			if item.MessageID == notice.MessageID {
				count++
				if item.Body != notice.Body || item.Seq != notice.Seq {
					t.Fatal("notice changed")
				}
			}
			if item.MessageID == batch {
				t.Fatal("background batch leaked into main")
			}
		}
		if count != 1 {
			t.Fatal("notice count", count)
		}
	}
	assertNotice(web)
	starts := len(businessStarts(agent))
	n.stop()
	srv = srv.restart()
	srv.startHistoricalMainThread(agent, background)
	reloaded := srv.webLogin(srv.startWeb().URL)
	assertNotice(reloaded)
	replay := reloaded.stream("?after=" + strconv.FormatInt(ev.EventSeq-1, 10))
	replay.until("same completion notice after restart", func(e protocol.LedgerEvent) bool { return e.Item != nil && e.Item.MessageID == notice.MessageID })
	if len(businessStarts(agent)) != starts {
		t.Fatal("restart replayed completed work")
	}
}
