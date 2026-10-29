package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestNamedPoolUnavailableModelsReleaseCapacityAndBackOff(t *testing.T) {
	testNamedPoolStartup(t, false)
}
func TestNamedPoolArchivePreparationBetweenSelectionAndStartup(t *testing.T) {
	testNamedPoolStartup(t, true)
}
func testNamedPoolStartup(t *testing.T, prepare bool) {
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "v1", Name: "v1", DefaultEffort: "high", SupportedEfforts: []string{"high"}}, {ID: "v2", Name: "v2", DefaultEffort: "high", SupportedEfforts: []string{"high"}}}
	e := newEnv(t, h)
	e.stop()
	execute := func(principal protocol.Principal, operation string, target protocol.Target, payload any, revision int64, command app.Command) protocol.Operation {
		t.Helper()
		raw, _ := json.Marshal(payload)
		expected := &revision
		if operation == "issues.create" || operation == "conversation.submit_message" {
			expected = nil
		}
		op, err := e.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw, ExpectedControlRevision: expected}, command)
		if err != nil || op.Status == protocol.OpFailed {
			t.Fatal(operation, op, err)
		}
		return op
	}
	decodeIssue := func(op protocol.Operation) protocol.Issue {
		t.Helper()
		var issue protocol.Issue
		if err := json.Unmarshal(op.Result, &issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	execute(owner, "categories.put", protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, 0, app.PutAgentCategory{})
	config := hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "hosted-executable", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}, Model: "v1", Effort: "high", Enabled: true}}}}
	accept := func(expected int64) {
		t.Helper()
		p, err := hostedconfig.Prepare(config)
		if err != nil {
			t.Fatal(err)
		}
		execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "hosted.configure", protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": p.Revision(), "hash": p.Hash()}, expected, app.AcceptHostedConfiguration{Prepared: p})
		e.app = app.New(e.store, e.ids, e.now, app.WithHostedConfiguration(p))
	}
	for _, id := range []string{"bad-one", "bad-two"} {
		d := config.Definitions.Agents[0]
		d.LocalID, d.Model = id, "missing-"+id
		config.Definitions.Agents = append(config.Definitions.Agents, d)
	}
	accept(0)
	conversations := []string{}
	requests := map[string]string{}
	for _, assignee := range []string{"bad-one", "bad-two", "planner"} {
		issue := decodeIssue(execute(owner, "issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.WorkItemPlan{Title: assignee, Project: "default", Intent: "Investigate this goal"}}, 0, app.CanonicalOwnerIssueCommand{Action: "create"}))
		issue = decodeIssue(execute(owner, "issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/" + assignee}, Reason: "Delegate investigation"}, issue.Revision, app.HostedIssueAssignment{}))
		issue = decodeIssue(execute(owner, "issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "No previous execution"}, issue.Revision, app.HostedIssueAssignment{Finish: true}))
		id := "issue:" + issue.IssueID
		conversations = append(conversations, id)
		op := execute(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: id, Body: "Investigate exactly this goal"}, 0, app.SubmitMessage{})
		var result struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(err)
		}
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM outbox WHERE destination=? AND operation='main_thread.handle_message' AND json_extract(payload_json,'$.message_id')=?`, "background_thread:"+id, result.MessageID).Scan(&result.MessageID)
		}); err != nil {
			t.Fatal(err)
		}
		requests[id] = result.MessageID
	}
	pendingBefore := map[string]store.OutboxMessage{}
	if prepare {
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			for _, id := range conversations {
				head, err := store.HeadOutboxPrioritized(t.Context(), tx.Q(), "background_thread:"+id, app.OpHandleEvent)
				if err != nil {
					return err
				}
				if head.RequestID != requests[id] {
					t.Fatal("fixture did not retain owner as queue head", head)
				}
				pendingBefore[id] = head
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	newManager := func(id string) *Manager {
		cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
		return New(e.app, cb, tools.New(e.app, e.store, cb, quiet()), Config{ConversationID: id, Instance: id, Thread: ThreadConfig(t.TempDir(), "unused"), Now: e.now, RestartDelay: 10 * time.Millisecond, Log: quiet(), OpenNamed: func(ctx context.Context, _ hostedconfig.Selection) (harness.Session, error) { return h.Open(ctx) }})
	}
	var healthyInputs atomic.Int32
	failures := make(chan string, 8)
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if !in.Repair && in.Model == "v1" {
			healthyInputs.Add(1)
		}
		invoke := func(name string, args any, out any) bool {
			r := th.CallTool(context.Background(), turn, name, name, args)
			var response struct {
				OK     bool            `json:"ok"`
				Result json.RawMessage `json:"result"`
			}
			if !r.Success || json.Unmarshal([]byte(r.Text), &response) != nil || !response.OK {
				failures <- fmt.Sprintf("%s: %s", name, r.Text)
				return false
			}
			if out != nil && json.Unmarshal(response.Result, out) != nil {
				failures <- "invalid response"
				return false
			}
			return true
		}
		var item protocol.IssueView
		if !invoke("issues_read", map[string]any{}, &item) {
			return "failed"
		}
		review := protocol.IssueContextReview{ExpectedAssignmentEpoch: item.AssignmentEpoch, ContextRevision: item.ContextRevision, Summary: "Investigation remains delegated"}
		for _, source := range item.SourceReviews {
			review.Sources = append(review.Sources, protocol.LogicalTurnReview{MessageID: source.MessageID, ContentRevision: source.ContentRevision, AssociationRevision: source.AssociationRevision})
		}
		if !invoke("issues_context_review", map[string]any{"effect_seq": 1, "payload": review}, nil) {
			return "failed"
		}
		var state protocol.LogicalTurnState
		if !invoke("issues_turn_read", map[string]any{}, &state) {
			return "failed"
		}
		commit := protocol.LogicalTurnCommit{Contexts: state.Contexts}
		for _, source := range state.Messages {
			commit.Messages = append(commit.Messages, protocol.LogicalTurnReview{MessageID: source.MessageID, ContentRevision: source.ContentRevision, AssociationRevision: source.Revision})
		}
		if !invoke("issues_turn_commit", map[string]any{"effect_seq": 2, "payload": commit}, nil) {
			return "failed"
		}
		return "completed"
	}

	selected := make(chan struct{}, 1)
	var once sync.Once
	opensBefore := h.Sessions()
	pool := &BackgroundPool{App: e.app, Limit: 2, New: func(id string) (*Manager, error) {
		if prepare {
			once.Do(func() { prepareArchive(t, e); selected <- struct{}{} })
		}
		return newManager(id), nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); pool.Run(ctx) }()
	defer func() { cancel(); <-done }()
	if prepare {
		select {
		case <-selected:
		case <-time.After(3 * time.Second):
			t.Fatal("pool did not select original pending input")
		}
		eventually(t, "selected-but-unstarted manager leaves pool after prepare", func() bool { drained, err := pool.ArchiveDrainStatus(t.Context()); return err == nil && drained })
		if h.Sessions() != opensBefore || len(h.Starts()) != 0 {
			t.Fatal("prepare opened a native process/turn", h.Sessions(), opensBefore, h.Starts())
		}
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			for _, id := range conversations {
				head, err := store.HeadOutboxPrioritized(t.Context(), tx.Q(), "background_thread:"+id, app.OpHandleEvent)
				if err != nil {
					return err
				}
				if head != pendingBefore[id] {
					t.Fatalf("prepare changed queued input: before=%+v after=%+v", pendingBefore[id], head)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	eventually(t, "third healthy Issue commits despite two unavailable models", func() bool {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		default:
		}
		state, err := e.app.LogicalTurn(t.Context(), requests[conversations[2]])
		return err == nil && state.Status == "completed" && healthyInputs.Load() > 0
	})
	pending, err := e.app.PendingAgentConversations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range conversations[:2] {
		if slices.Contains(pending, id) {
			t.Fatal("deferred head rediscovered before its retry deadline", id)
		}
		if n := e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id=?`, requests[id]); n != 0 {
			t.Fatal("known startup failure acquired a native identity", n)
		}
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var status string
			var attempts, next int64
			var body string
			err := tx.Q().QueryRowContext(t.Context(), `SELECT o.status,o.attempts,o.next_attempt_at,m.body FROM outbox o JOIN conversation_messages m ON m.message_id=json_extract(o.payload_json,'$.message_id') WHERE o.request_id=?`, requests[id]).Scan(&status, &attempts, &next, &body)
			if err == nil && (body != "Investigate exactly this goal" || status != "pending" || attempts != 0 || next <= e.now().UnixMilli()) {
				t.Fatalf("unavailable input identity lost or not deferred: %s %d %d", status, attempts, next)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}
