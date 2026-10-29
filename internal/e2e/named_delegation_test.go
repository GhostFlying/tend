package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

// TestEndToEnd_FakeHarness exercises the current public capability path:
// named main delegates, a scoped manager freezes the task, and actual runner
// HTTP performs implementation and independent acceptance through two generations.
func TestEndToEnd_FakeHarness(t *testing.T) {
	srv := newNamedFixtureServer(t, "fixture-model", "FIXTURE_NATIVE")
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "fixture-model", Name: "fixture-native", SupportedEfforts: []string{"high"}, DefaultEffort: "high"}}
	failures := make(chan string, 16)
	managerReviewed := make(chan struct{})
	var reviewedOnce sync.Once
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		call := 0
		invoke := func(name string, args any, out any) bool {
			call++
			res := th.CallTool(context.Background(), turn, fmt.Sprintf("fixture-%d", call), name, args)
			var answer struct {
				OK     bool            `json:"ok"`
				Result json.RawMessage `json:"result"`
			}
			if !res.Success || json.Unmarshal([]byte(res.Text), &answer) != nil || !answer.OK {
				select {
				case failures <- name + ": " + res.Text:
				default:
				}
				return false
			}
			if out != nil {
				if err := json.Unmarshal(answer.Result, out); err != nil {
					select {
					case failures <- name + ": " + err.Error():
					default:
					}
					return false
				}
			}
			return true
		}
		manager := false
		for _, spec := range th.Config.Tools {
			if spec.Name == "issues_turn_read" {
				manager = true
			}
			if spec.Name == "tasks_create" {
				failures <- "retired task planning advertised"
				return "failed"
			}
		}
		if !manager {
			var state protocol.LogicalTurnState
			if !invoke("conversation_turn_read", map[string]any{}, &state) {
				return "failed"
			}
			// Ledger continuations and repairs only reconcile their original responsibility.
			if !in.Repair && len(state.Messages) > 0 && func() bool {
				message, err := srv.app.Message(context.Background(), in.UserMessageID)
				return err == nil && message.Author == "owner"
			}() {
				source := state.Messages[0]
				revision := source.ContentRevision
				payload := protocol.CreateIssuePayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source.MessageID, ExpectedSourceRevision: &revision}, Assignee: &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: hostedconfig.IdentityRef(protocol.AgentIdentity{ExecutionEnvironmentID: "fixture", LocalID: "manager"})}, Plan: protocol.IssuePlan{Title: "Fix the build", Project: "default", Intent: "Delegate implementation and independently check the result"}}
				if !invoke("issues_create", map[string]any{"effect_seq": 1, "payload": payload}, nil) {
					return "failed"
				}
				th.Say(turn, "delegated", "The Issue manager owns the build investigation and task.")
				eventually(t, "native reply persisted before metadata review", func() bool {
					return srv.count(`SELECT COUNT(*) FROM conversation_messages WHERE reply_to=? AND body=?`, source.MessageID, "The Issue manager owns the build investigation and task.") == 1
				})
			}
			if !invoke("conversation_turn_read", map[string]any{}, &state) {
				return "failed"
			}
			for _, message := range state.Messages {
				if message.Disposition == "pending" {
					ids := []string{}
					for _, item := range message.Items {
						ids = append(ids, item.WorkItemID)
					}
					disposition := "unassigned"
					if len(ids) > 0 {
						disposition = "linked"
					}
					if !invoke("conversation_associate_issues", map[string]any{"effect_seq": 100 + call, "target_id": message.MessageID, "expected_control_revision": message.Revision, "payload": protocol.SetIssueMessageAssociationsPayload{ContentRevision: message.ContentRevision, Disposition: disposition, IssueIDs: ids}}, nil) {
						return "failed"
					}
				}
			}
			if !invoke("conversation_turn_read", map[string]any{}, &state) {
				return "failed"
			}
			commit := protocol.LogicalTurnCommit{Contexts: state.Contexts}
			for _, message := range state.Messages {
				commit.Messages = append(commit.Messages, protocol.LogicalTurnReview{MessageID: message.MessageID, ContentRevision: message.ContentRevision, AssociationRevision: message.Revision})
			}
			if !invoke("conversation_turn_commit", commit, nil) {
				return "failed"
			}
			return "completed"
		}
		if th.Config.Sandbox != "read-only" || th.Config.Config["features.shell_tool"] != false {
			failures <- "manager retained native execution"
			return "failed"
		}
		var item protocol.IssueView
		if !invoke("issues_read", map[string]any{}, &item) {
			return "failed"
		}
		seq := int64(1)
		if len(item.Tasks) == 0 && !in.Repair {
			task := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: item.AssignmentEpoch, Title: "Fix the build", WorkspaceName: "fix-build", Rating: protocol.RatingP2, Spec: protocol.OwnerTaskSpec{Goal: "fix the build", Acceptance: []string{"build passes"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}}}
			if !invoke("issues_create_task", map[string]any{"effect_seq": seq, "expected_control_revision": item.Revision, "payload": task}, nil) {
				return "failed"
			}
			seq++
		}
		if !invoke("issues_read", map[string]any{}, &item) {
			return "failed"
		}
		if len(item.PendingReviews) > 0 {
			reviews := protocol.ReviewIssueCompletionsPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: item.AssignmentEpoch}}
			for _, review := range item.PendingReviews {
				reviews.Reviews = append(reviews.Reviews, protocol.CompletionReviewAcknowledgement{EventID: review.EventID, Reason: "The original independent acceptance passed; retain its result"})
			}
			if !invoke("issues_review_completions", map[string]any{"effect_seq": seq, "expected_control_revision": item.Revision, "payload": reviews}, nil) {
				return "failed"
			}
			seq++
			if !invoke("issues_read", map[string]any{}, &item) {
				return "failed"
			}
		}
		review := protocol.IssueContextReview{ExpectedAssignmentEpoch: item.AssignmentEpoch, ContextRevision: item.ContextRevision, Summary: "Reviewed the exact source and current task/acceptance facts"}
		for _, source := range item.SourceReviews {
			review.Sources = append(review.Sources, protocol.LogicalTurnReview{MessageID: source.MessageID, ContentRevision: source.ContentRevision, AssociationRevision: source.AssociationRevision})
		}
		if !invoke("issues_context_review", map[string]any{"effect_seq": seq, "payload": review}, nil) {
			return "failed"
		}
		seq++
		var state protocol.LogicalTurnState
		if !invoke("issues_turn_read", map[string]any{}, &state) {
			return "failed"
		}
		commit := protocol.LogicalTurnCommit{Contexts: state.Contexts}
		for _, message := range state.Messages {
			commit.Messages = append(commit.Messages, protocol.LogicalTurnReview{MessageID: message.MessageID, ContentRevision: message.ContentRevision, AssociationRevision: message.Revision})
		}
		if !invoke("issues_turn_commit", map[string]any{"effect_seq": seq, "payload": commit}, nil) {
			return "failed"
		}
		reviewedOnce.Do(func() { close(managerReviewed) })
		return "completed"
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	srv.onStop(func() { cancel(); wg.Wait() })
	cb := contextbuilder.New(srv.app, srv.store, srv.ids, contextbuilder.Config{})
	service := tools.New(srv.app, srv.store, cb, quiet())
	makeManager := func(conversation string) *mainthread.Manager {
		return mainthread.New(srv.app, cb, service, mainthread.Config{Instance: "fixture/" + conversation, ConversationID: conversation, Thread: mainthread.ThreadConfig(t.TempDir(), "unused"), OpenNamed: func(ctx context.Context, selection hostedconfig.Selection) (harness.Session, error) {
			if selection.Runtime.ExecutableRef != "FIXTURE_NATIVE" {
				return nil, fmt.Errorf("wrong runtime")
			}
			return h.Open(ctx)
		}, RestartDelay: 20 * time.Millisecond, Log: quiet()})
	}
	front := makeManager("")
	pool := &mainthread.BackgroundPool{App: srv.app, Front: front, New: func(id string) (*mainthread.Manager, error) { return makeManager(id), nil }}
	dispatcher := dispatch.New(srv.app, map[string]dispatch.Handler{app.MainThreadDestination: front, app.MainThreadControlDestination: pool}, srv.signal, time.Now, dispatch.Config{PollInterval: 20 * time.Millisecond}, quiet())
	wg.Go(func() { _ = front.Run(ctx) })
	wg.Go(func() { pool.Run(ctx) })
	wg.Go(func() { _ = dispatcher.Run(ctx) })
	var mu sync.Mutex
	verdicts := []protocol.Verdict{protocol.VerdictChangesRequired, protocol.VerdictPassed}
	n := newNode(t, srv, func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role != protocol.RoleAccept {
			return passing(spec)
		}
		mu.Lock()
		v := verdicts[0]
		verdicts = verdicts[1:]
		mu.Unlock()
		return testkit.WorkerScript{Result: harness.WorkerResult{Verdict: &protocol.VerdictPayload{Verdict: v, Evidence: json.RawMessage(`{}`)}}}
	})
	srv.say("The build is broken, please fix it.")
	eventually(t, "initial manager context commits before workers run", func() bool {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		default:
		}
		select {
		case <-managerReviewed:
			return srv.count(`SELECT COUNT(*) FROM logical_turns WHERE status<>'completed'`) == 0
		default:
			return false
		}
	})
	n.start()
	eventually(t, "named delegation completes with independent acceptance", func() bool {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		default:
		}
		return srv.count(`SELECT COUNT(*) FROM tasks WHERE lifecycle='completed'`) == 1
	})
	eventually(t, "completion review reaches the original Issue manager", func() bool {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		default:
		}
		return srv.count(`SELECT COUNT(*) FROM work_item_completion_reviews WHERE reviewed_revision IS NULL`) == 0 && srv.count(`SELECT COUNT(*) FROM logical_turns WHERE status<>'completed'`) == 0
	})
	for query, want := range map[string]int{`SELECT COUNT(*) FROM issue_controls`: 1, `SELECT COUNT(*) FROM tasks`: 1, `SELECT COUNT(*) FROM runs WHERE role='implement'`: 2, `SELECT COUNT(*) FROM runs WHERE role='accept'`: 2, `SELECT COUNT(*) FROM artifacts`: 2, `SELECT COUNT(*) FROM acceptances`: 2, `SELECT COUNT(*) FROM task_work_item_owners`: 1, `SELECT COUNT(*) FROM (SELECT effect_key FROM plan_actions GROUP BY effect_key HAVING COUNT(*)>1)`: 0, `SELECT COUNT(*) FROM (SELECT event_id FROM events GROUP BY event_id HAVING COUNT(*)>1)`: 0} {
		if got := srv.count(query); got != want {
			t.Errorf("%s=%d,want%d", query, got, want)
		}
	}
	if srv.count(`SELECT COUNT(*) FROM hosted_executions WHERE role='main'`) < 1 || srv.count(`SELECT COUNT(*) FROM hosted_executions WHERE role='issue_manager'`) < 1 {
		t.Fatal("task was not delegated through both named roles")
	}
}

// Both fake and real harness integration use the same accepted named roles.
func newNamedFixtureServer(t *testing.T, model, executable string) *server {
	t.Helper()
	configuration := hostedconfig.Configuration{Revision: 1, EnvironmentID: "fixture", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: executable, Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}}, Agents: []agentconfig.AgentDefinition{
		{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: model, Effort: "high", Enabled: true},
		{LocalID: "manager", Revision: 1, DisplayName: "Issue manager", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: model, Effort: "high", Enabled: true},
	}}}
	prepared, err := hostedconfig.Prepare(configuration)
	if err != nil {
		t.Fatal(err)
	}
	srv := openServerWithOptions(t, time.Minute, filepath.Join(t.TempDir(), "tend.db"), time.Now, app.WithHostedConfiguration(prepared))
	zero := int64(0)
	execute := func(principal protocol.Principal, operation string, target protocol.Target, payload any, command app.Command) {
		t.Helper()
		body, _ := json.Marshal(payload)
		op, err := srv.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: operation, Target: target, ExpectedControlRevision: &zero, Payload: body}, command)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(operation, op, err)
		}
	}
	execute(owner, "categories.put", protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, app.PutAgentCategory{})
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "hosted.configure", protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, app.AcceptHostedConfiguration{Prepared: prepared})

	return srv
}
