package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestEndToEnd_CompletionNoticeWakesMainAndPersistsAcrossRestart(t *testing.T) {
	srv := newServer(t, time.Minute)
	agent := testkit.NewHarness()
	agent.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if strings.Contains(in.Text, "create completion check") {
			res := th.CallTool(context.Background(), turn, "create", "tasks_create", map[string]any{"title": "notice task", "commit_seq": 1, "workspace_name": "notice-task", "spec": domain.Spec{Goal: "notice task", Acceptance: []string{"works"}, Delivery: domain.DeliveryCommit, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}}})
			if !res.Success {
				return "failed"
			}
		}
		th.Say(turn, "reply", "The ledger is the source of truth.")
		return "completed"
	}
	background := func(th *testkit.FakeThread, turn string, _ harness.TurnInput) string {
		item := strings.TrimPrefix(th.Config.Cwd, "/background/work-item:")
		sources := th.CallTool(context.Background(), turn, "sources", "background_sources", map[string]any{})
		if !sources.Success {
			return "failed"
		}
		result := th.CallTool(context.Background(), turn, "context", "work_item_context_read", map[string]any{"work_item_id": item})
		var contextView struct {
			OK     bool                     `json:"ok"`
			Result protocol.WorkItemContext `json:"result"`
		}
		if !result.Success || json.Unmarshal([]byte(result.Text), &contextView) != nil || !contextView.OK {
			return "failed"
		}
		th.Say(turn, "review", "Completion evidence retained; coordinator review required.")
		result = th.CallTool(context.Background(), turn, "assessment", "background_submit", protocol.BackgroundAssessmentPayload{ContextRevision: contextView.Result.Revision, Disposition: "escalate", Note: "Completion requires coordinator review", EvidenceIDs: []string{}})
		if !result.Success {
			return "failed"
		}
		return "completed"
	}
	srv.startMainThread(agent, background)
	web := srv.webLogin(srv.startWeb().URL)
	stream := web.stream("")
	n := newNode(t, srv, passing)
	n.start()
	web.send(protocol.SubmitMessagePayload{Body: "create completion check"})
	events := stream.until("system completion notice", func(e protocol.LedgerEvent) bool {
		return e.Item != nil && e.Item.Author == "system" && strings.Contains(e.Item.Body, commitSHA)
	})
	ev := events[len(events)-1]
	notice := *ev.Item
	if notice.Kind != protocol.ItemMessage || notice.ReplyTo != "" || !strings.Contains(notice.Body, "spec 1") {
		t.Fatal(notice)
	}
	eventually(t, "structured background escalation and main review settle", func() bool {
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
	srv.startMainThread(agent, background)
	reloaded := srv.webLogin(srv.startWeb().URL)
	assertNotice(reloaded)
	replay := reloaded.stream("?after=" + strconv.FormatInt(ev.EventSeq-1, 10))
	replay.until("same completion notice after restart", func(e protocol.LedgerEvent) bool { return e.Item != nil && e.Item.MessageID == notice.MessageID })
	if len(businessStarts(agent)) != starts {
		t.Fatal("restart replayed completed work")
	}
}
