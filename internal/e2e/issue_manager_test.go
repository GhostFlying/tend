package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

type managerTestResolver struct {
	pin       protocol.RunExecutionPin
	native    *testkit.Harness
	lostStart *atomic.Bool
}

func (m *managerTestResolver) Refresh(context.Context) error { return nil }
func (m *managerTestResolver) Observe(context.Context) ([]protocol.AgentExecutor, error) {
	return []protocol.AgentExecutor{{Pin: m.pin, Available: true}}, nil
}
func (m *managerTestResolver) Resolve(context.Context, protocol.RunExecutionPin) (runner.ResolvedExecution, error) {
	return runner.ResolvedExecution{}, errors.New("manager cannot execute a worker contract")
}
func (m *managerTestResolver) ResolveManager(_ context.Context, p protocol.RunExecutionPin) (runner.ManagerExecution, error) {
	if p != m.pin {
		return runner.ManagerExecution{}, errors.New("manager pin changed")
	}
	return runner.ManagerExecution{Profile: p.Profile, Open: func(ctx context.Context, _ string) (harness.Session, error) {
		s, err := m.native.Open(ctx)
		if err != nil {
			return nil, err
		}
		return managerPermissionSession{FakeSession: s.(*testkit.FakeSession), lostStart: m.lostStart}, nil
	}}, nil
}

type managerPermissionSession struct {
	*testkit.FakeSession
	lostStart *atomic.Bool
}
type managerPermissionThread struct {
	*testkit.FakeThread
	events    <-chan harness.Event
	lostStart *atomic.Bool
}

func (t managerPermissionThread) Events() <-chan harness.Event { return t.events }

func (t managerPermissionThread) StartTurn(ctx context.Context, input harness.TurnInput) (string, error) {
	id, err := t.FakeThread.StartTurn(ctx, input)
	if err == nil && t.lostStart != nil && t.lostStart.CompareAndSwap(false, true) {
		return "", errors.New("native start receipt lost; original history is retained")
	}
	return id, err
}

func managerThreadWithClosingUsage(ctx context.Context, t *testkit.FakeThread) managerPermissionThread {
	events := make(chan harness.Event)
	go func() {
		defer close(events)
		for event := range t.Events() {
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
		// Deterministically arrive after delivery cancellation, during Close.
		select {
		case events <- harness.Event{Kind: harness.EventRequestUsage, RequestUsage: &protocol.TokenUsageSample{Source: "native-close", RequestID: "close-" + t.ID(), ThreadID: t.ID(), Revision: 1, Settlement: "final", Model: "manager-model"}}:
		case <-ctx.Done():
		}
	}()
	return managerPermissionThread{FakeThread: t, events: events}
}

// The single-slot path exercises workspace release as well as worker execution.
type managerWorkspaces struct{ *workspaces }

func (managerWorkspaces) Cleanup(context.Context, protocol.WorkspaceCleanup) error { return nil }

func (t managerPermissionThread) PermissionDefaults() harness.PermissionSettings {
	return harness.PermissionSettings{Sandbox: t.Config.Sandbox, ApprovalPolicy: t.Config.ApprovalPolicy}
}
func (s managerPermissionSession) StartThread(ctx context.Context, c harness.ThreadConfig) (harness.Thread, error) {
	t, err := s.FakeSession.StartThread(ctx, c)
	if err != nil {
		return nil, err
	}
	thread := managerThreadWithClosingUsage(ctx, t.(*testkit.FakeThread))
	thread.lostStart = s.lostStart
	return thread, nil
}
func (s managerPermissionSession) ResumeThread(ctx context.Context, id string, c harness.ThreadConfig) (harness.Thread, error) {
	t, err := s.FakeSession.ResumeThread(ctx, id, c)
	if err != nil {
		return nil, err
	}
	thread := managerThreadWithClosingUsage(ctx, t.(*testkit.FakeThread))
	thread.lostStart = s.lostStart
	return thread, nil
}

func TestEndToEndRunnerManagerClaimsReviewsAndReleasesSlot(t *testing.T) {
	runRunnerManager(t, false, false, false)
}
func TestEndToEndSingleSlotManagerDelegatesAcceptsAndCloses(t *testing.T) {
	runRunnerManager(t, true, false, false)
}
func TestFault_RunnerManagerRateLimitContinuesWithoutRepeatingTools(t *testing.T) {
	runRunnerManager(t, false, true, false)
}
func TestFault_ManagerUnknownStartReconcilesAfterClosingItsProcess(t *testing.T) {
	runRunnerManager(t, false, false, true)
}
func runRunnerManager(t *testing.T, delivery, recoverRate, loseStart bool) {
	s := openServerWithOptions(t, time.Second*10, filepath.Join(t.TempDir(), "server.db"), time.Now, app.WithFollowups(true))
	if delivery {
		s.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}}, Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Slots: 1, Repos: []string{"tend"}, TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:tend-runner"}}}}, app.SyncConfig{})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wg sync.WaitGroup
	category := protocol.DefaultIssueManagerCategory
	zero := int64(0)
	execute := func(op string, target protocol.Target, payload any, revision *int64, command app.Command) protocol.Operation {
		raw, _ := json.Marshal(payload)
		out, err := s.app.Execute(ctx, owner, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: op, Target: target, Payload: raw, ExpectedControlRevision: revision}, command)
		if err != nil || out.Status != protocol.OpSucceeded {
			t.Fatalf("%s: %+v %v", op, out, err)
		}
		return out
	}
	execute("categories.put", protocol.Target{Type: "category", ID: category}, protocol.PutAgentCategoryPayload{Title: "Issue managers", State: "active"}, &zero, app.PutAgentCategory{})
	definitions, err := agentconfig.Validate("linux-1", agentconfig.Config{FormatVersion: 2, Runtimes: []agentconfig.RuntimeBinding{{ID: "native-manager", Revision: 1, Kind: agentconfig.AppServerWorker, CLI: "codex", ExecutableRef: "fixture-native", DeclaredPurposes: []agentconfig.Purpose{agentconfig.IssueManager}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "manager", Revision: 1, DisplayName: "Manager", RuntimeID: "native-manager", Categories: []string{category}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: "manager-model", Enabled: true}}}, agentconfig.SourceReference{Kind: agentconfig.Inline})
	if err != nil {
		t.Fatal(err)
	}
	projection := definitions.Public()
	_, hash, err := definitions.CanonicalPublic()
	if err != nil {
		t.Fatal(err)
	}
	fake := testkit.NewHarness()
	fake.Models = []harness.Model{{ID: "manager-model", Name: "manager-model"}}
	failures := make(chan string, 8)
	var attempts, continuations, effects atomic.Int64
	var rateFailed atomic.Bool
	fake.Turn = func(th *testkit.FakeThread, turn string, input harness.TurnInput) string {
		attempts.Add(1)
		if loseStart && input.Repair && s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 0 {
			failures <- "metadata repair started before original native receipts reconciled"
			return "failed"
		}
		if !delivery && !recoverRate && !loseStart && !input.Repair {
			image, _ := json.Marshal(map[string]any{"id": "large-image-item", "type": "userMessage", "content": []any{map[string]any{"type": "image", "url": "data:image/png;base64," + strings.Repeat("A", 5<<20)}}})
			th.Emit(harness.Event{Kind: harness.EventItemCompleted, TurnID: turn, ItemID: "large-image-item", ItemType: harness.ItemUserMessage, Raw: image})
		}
		if recoverRate && input.Continue {
			continuations.Add(1)
			if input.Text != "" {
				failures <- fmt.Sprintf("rate limit recovery resent the original owner input: %+v", input)
				return "failed"
			}
			th.Say(turn, "manager-reply", "Continued the same responsibility with previously completed tool results")
			return "completed"
		}
		n := 0
		invoke := func(name string, args, out any) bool {
			n++
			result := th.CallTool(ctx, turn, fmt.Sprintf("call-%d", n), name, args)
			var answer struct {
				OK     bool            `json:"ok"`
				Result json.RawMessage `json:"result"`
			}
			if !result.Success || json.Unmarshal([]byte(result.Text), &answer) != nil || !answer.OK {
				if loseStart && !input.Repair && !result.Success && result.Text == "Coordinator native process closed before tool result" {
					return false
				}
				failures <- name + ": " + result.Text
				return false
			}
			if out != nil && json.Unmarshal(answer.Result, out) != nil {
				failures <- "decode " + name
				return false
			}
			return true
		}
		var item protocol.IssueView
		if !invoke("issues_read", map[string]any{}, &item) {
			return "failed"
		}
		review := protocol.IssueContextReview{ExpectedAssignmentEpoch: item.AssignmentEpoch, ContextRevision: item.ContextRevision, Summary: "Reviewed the continuing manager responsibility"}
		for _, source := range item.SourceReviews {
			review.Sources = append(review.Sources, protocol.LogicalTurnReview{MessageID: source.MessageID, ContentRevision: source.ContentRevision, AssociationRevision: source.AssociationRevision})
		}
		seq := int64(0)
		mutate := func(name string, payload any, revision *int64, out any) bool {
			seq = effects.Add(1)
			args := map[string]any{"effect_seq": seq, "payload": payload}
			if revision != nil {
				args["expected_control_revision"] = *revision
			}
			return invoke(name, args, out)
		}
		if !mutate("issues_context_review", review, nil, nil) {
			return "failed"
		}
		if delivery && len(item.Tasks) == 0 {
			contract := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: item.AssignmentEpoch, Title: "Investigate the delegated intent", Spec: protocol.OwnerTaskSpec{Goal: "Return evidence for the responsibility", Acceptance: []string{"Independent review checks the evidence"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}
			if !mutate("issues_create_task", contract, &item.Revision, nil) {
				return "failed"
			}
			if !invoke("issues_read", map[string]any{}, &item) {
				return "failed"
			}
			review.ContextRevision = item.ContextRevision
			review.Sources = nil
			for _, source := range item.SourceReviews {
				review.Sources = append(review.Sources, protocol.LogicalTurnReview{MessageID: source.MessageID, ContentRevision: source.ContentRevision, AssociationRevision: source.AssociationRevision})
			}
			if !mutate("issues_context_review", review, nil, nil) {
				return "failed"
			}

		}

		var state protocol.LogicalTurnState
		if !invoke("issues_turn_read", map[string]any{}, &state) {
			return "failed"
		}
		commit := protocol.LogicalTurnCommit{Contexts: state.Contexts}
		for _, message := range state.Messages {
			commit.Messages = append(commit.Messages, protocol.LogicalTurnReview{MessageID: message.MessageID, ContentRevision: message.ContentRevision, AssociationRevision: message.Revision})
		}
		if !mutate("issues_turn_commit", commit, nil, nil) {
			return "failed"
		}
		released := true
		if delivery && len(item.Tasks) > 0 {
			var task protocol.TaskDetail
			if !invoke("tasks_read", map[string]any{"task_id": item.Tasks[0].TaskID}, &task) {
				return "failed"
			}
			released = task.WorkspaceState == nil || task.WorkspaceState.OccupiedBy == ""
		}
		if delivery && len(item.PendingReviews) > 0 && released {
			if !invoke("issues_read", map[string]any{}, &item) {
				return "failed"
			}
			acknowledgements := []protocol.CompletionReviewAcknowledgement{}
			for _, pending := range item.PendingReviews {
				acknowledgements = append(acknowledgements, protocol.CompletionReviewAcknowledgement{EventID: pending.EventID, Reason: "Read the passed independent acceptance and checked remaining scope"})
			}
			close := protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: item.AssignmentEpoch, Reason: "Delegated evidence passed independent acceptance"}, CompletionReviews: acknowledgements, Closure: &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "The goal has independent accepted evidence", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceAcceptance, Reference: item.PendingReviews[0].AcceptanceID, Summary: "Worker's independent acceptance passed"}}}}
			if !mutate("issues_close", close, &item.Revision, nil) {
				return "failed"
			}
		}

		if recoverRate && !input.Repair && rateFailed.CompareAndSwap(false, true) {
			th.Emit(harness.Event{Kind: harness.EventTurnError, TurnID: turn, Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}})
			return "failed"
		}
		th.Say(turn, "manager-reply", "Manager responsibility is ready; waiting for new evidence")
		return "completed"
	}
	pin := protocol.RunExecutionPin{Purpose: "issue_manager", Identity: projection.Agents[0].Identity, AgentRevision: 1, Runtime: projection.Agents[0].Binding, InventoryRevision: 1, SnapshotID: "manager-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: "manager-model"}, Worker: protocol.WorkerAppServer}
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), s.now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); wg.Wait(); j.Close() }()
	resolver := &managerTestResolver{pin: pin, native: fake}
	if loseStart {
		resolver.lostStart = &atomic.Bool{}
	}
	worker := runner.New(runner.Config{ServerURL: s.url, HostID: "linux-1", HostToken: hostToken, Journal: j, Workers: &testkit.Workers{Script: func(spec harness.WorkerSpec) testkit.WorkerScript {
		if spec.Role == protocol.RoleImplement && delivery {
			return testkit.WorkerScript{Result: harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Verified evidence for the delegated goal"}}}}
		}
		return passing(spec)
	}}, Workspaces: managerWorkspaces{&workspaces{dir: t.TempDir()}}, Executions: resolver, AgentInventory: func(context.Context) (protocol.LocalAgentConfiguration, error) {
		return protocol.LocalAgentConfiguration{State: "accepted", Revision: 1, SnapshotID: pin.SnapshotID, PublicSHA256: hash, Projection: &projection}, nil
	}, Now: s.now, IDs: protocol.NewULIDGen(s.now, nil), Log: quiet(), RetryDelay: time.Millisecond * 10, HeartbeatInterval: time.Millisecond * 50})
	wg.Go(func() { _ = worker.Run(ctx) })
	cb := contextbuilder.New(s.app, s.store, s.ids, contextbuilder.Config{})
	pool := &mainthread.BackgroundPool{App: s.app, Limit: 1, New: func(id string) (*mainthread.Manager, error) {
		return mainthread.New(s.app, cb, tools.New(s.app, s.store, cb, quiet()), mainthread.Config{Instance: "runner-manager/" + id, ConversationID: id, Thread: mainthread.ThreadConfig("unused-server-directory", ""), OpenNamed: func(ctx context.Context, selected hostedconfig.Selection) (harness.Session, error) {
			if !selected.RunnerManaged || selected.RunnerRunID == "" {
				return nil, errors.New("server-local manager launch is forbidden")
			}
			return mainthread.RunnerSession(ctx, s.app, selected.RunnerRunID), nil
		}, RestartDelay: time.Millisecond * 20, Log: quiet()}), nil
	}}
	wg.Go(func() { pool.Run(ctx) })
	created := execute("issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Coordinate an intent", Project: "default", Intent: "Retain continuing responsibility"}}, nil, app.CanonicalOwnerIssueCommand{Action: "create"})
	var issue protocol.Issue
	if err = json.Unmarshal(created.Result, &issue); err != nil {
		t.Fatal(err)
	}
	execute(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Delegate by category"}, &issue.Revision, app.RequestIssueManager{})
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case failure := <-failures:
			t.Fatal(failure)
		case <-deadline.C:
			_ = s.store.Read(context.WithoutCancel(ctx), func(tx *store.ReadTx) error {
				for _, query := range []string{
					`SELECT json_object('run',run_id,'role',role,'state',observed_state) FROM runs`,
					`SELECT json_object('task',task_id,'lifecycle',lifecycle) FROM tasks`,
					`SELECT json_object('request',request_id,'op',operation,'status',status) FROM outbox WHERE destination LIKE 'background_thread:%'`,
					`SELECT json_object('method',method,'status',status,'result',result_json) FROM manager_native_requests ORDER BY created_at`,
					`SELECT json_object('workspace',workspace_id,'occupied_by',occupied_by) FROM workspaces`,
				} {
					rows, err := tx.Q().QueryContext(ctx, query)
					if err != nil {
						return err
					}
					for rows.Next() {
						var value string
						if err := rows.Scan(&value); err != nil {
							rows.Close()
							return err
						}
						t.Log(value)
					}
					if err := rows.Close(); err != nil {
						return err
					}
				}
				return nil
			})
			t.Fatalf("manager did not finish: runs=%d native=%d replies=%d", s.count(`SELECT COUNT(*) FROM runs`), s.count(`SELECT COUNT(*) FROM manager_native_requests`), s.count(`SELECT COUNT(*) FROM conversation_messages WHERE author='assistant'`))
		case <-time.After(time.Millisecond * 20):
		}
		if delivery {
			if s.count(`SELECT COUNT(*) FROM issue_controls WHERE issue_id=? AND state='closed'`, issue.IssueID) == 1 && s.count(`SELECT COUNT(*) FROM issue_manager_tasks WHERE issue_id=? AND state='completed'`, issue.IssueID) == 1 {
				break
			}
		} else if s.count(`SELECT COUNT(*) FROM issue_manager_tasks m JOIN tasks t USING(task_id) WHERE m.issue_id=? AND m.state='assigned' AND t.lifecycle='waiting'`, issue.IssueID) == 1 && (!recoverRate || continuations.Load() == 1) && s.count(`SELECT COUNT(*) FROM runs WHERE role='issue_manager' AND observed_state='finished'`) > 0 && s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending`) == 0 && s.count(`SELECT COUNT(*) FROM leases WHERE status='active'`) == 0 && s.count(`SELECT COUNT(*) FROM outbox WHERE destination LIKE 'background_thread:%' AND status IN ('pending','sending','unknown')`) == 0 {
			break
		}
	}
	if s.count(`SELECT COUNT(*) FROM runs WHERE role='issue_manager' AND observed_state='finished'`) < 1 || s.count(`SELECT COUNT(*) FROM leases WHERE status='active'`) != 0 || (!delivery && s.count(`SELECT COUNT(*) FROM workspaces`) != 0) {
		t.Fatal("manager did not release its execution resources")
	}
	if recoverRate && (continuations.Load() != 1 || attempts.Load() < 2 || fake.Resumed() == 0 || s.count(`SELECT COUNT(*) FROM agent_recoveries WHERE status='completed'`) != 1) {
		t.Fatal("rate limit continuation did not retain its original session and budget")
	}
	if loseStart {
		for _, input := range fake.Starts()[1:] {
			if !input.Repair {
				t.Fatal("unknown start resent business input instead of reconciling history")
			}
		}
		if fake.Resumed() == 0 || s.count(`SELECT COUNT(*) FROM runs WHERE role='issue_manager' AND observed_state='failed'`) == 0 {
			t.Fatal("unknown start did not release capacity for original-history reconciliation")
		}
	}
	if delivery && (s.count(`SELECT COUNT(*) FROM runs WHERE role='implement'`) != 1 || s.count(`SELECT COUNT(*) FROM runs WHERE role='accept'`) != 1 || s.count(`SELECT COUNT(*) FROM acceptances WHERE verdict='passed'`) != 1) {
		t.Fatal("single slot did not finish independent delivery and acceptance")
	}
	if s.count(`SELECT COUNT(*) FROM issue_controls WHERE issue_id=? AND assignee_ref='linux-1/manager'`, issue.IssueID) != 1 {
		t.Fatal("claim did not set actual manager identity")
	}
	if s.count(`SELECT COUNT(*) FROM token_usage_current WHERE source='native-close'`) == 0 || s.count(`SELECT COUNT(*) FROM manager_native_events WHERE applied=0`) != 0 {
		t.Fatal("final native usage was not applied before manager slot release")
	}
	if !delivery && !recoverRate && !loseStart && s.count(`SELECT COUNT(*) FROM manager_native_events WHERE body_json LIKE '%tend:inline-image-url:sha256:%' AND length(body_json)<2048`) == 0 {
		t.Fatal("large native image evidence did not cross the authenticated runner RPC")
	}
}
