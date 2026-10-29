package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestNamedPlannerDelegatesToActualIsolatedManagerTools(t *testing.T) {
	testNamedManagerTools(t, false, false, false, false)
}
func TestNamedPlannerTransfersResponsibilityToActualOwner(t *testing.T) {
	testNamedManagerTools(t, true, false, false, false)
}
func TestNamedManagerControlsOnlyPrimaryTask(t *testing.T) {
	testNamedManagerTools(t, false, true, false, false)
}
func TestNamedPlannerRepairRejectsBusinessMutation(t *testing.T) {
	testNamedManagerTools(t, false, false, true, false)
}
func TestNamedManagerRepairRejectsBusinessMutation(t *testing.T) {
	testNamedManagerTools(t, false, false, false, true)
}
func TestNamedManagerReadsOwnBackgroundSources(t *testing.T) {
	testNamedManagerTools(t, false, false, false, false, true)
}
func TestNamedPlannerRepairClassifiesCanonicalSource(t *testing.T) {
	testNamedManagerTools(t, false, false, true, false, false, true)
}
func TestNamedPlannerClassifiesCanonicalSourceAndCommits(t *testing.T) {
	testNamedManagerTools(t, false, false, false, false, false, true)
}
func testNamedManagerTools(t *testing.T, ownerTransfer, taskControls, repairMode, managerRepair bool, readSources ...bool) {
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "manager-model", Name: "native-manager-model", SupportedEfforts: []string{"high"}, DefaultEffort: "high"}}
	var e *env
	var otherTask string
	allowManagers := make(chan struct{})
	var allowOnce sync.Once
	managerDone := make(chan string, 1)
	failed := make(chan string, 4)
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if in.Repair && managerRepair && (!strings.Contains(in.Text, "issues_turn_read") || !strings.Contains(in.Text, "issues_context_review") || strings.Contains(in.Text, "conversation_turn_commit")) {
			failed <- "repair instruction does not match Issue manager tools: " + in.Text
			return "failed"
		}
		if in.Repair && repairMode && strings.Contains(in.Text, "initialize missing work-item context") {
			failed <- "named main repair requests a technical context baseline"
			return "failed"
		}
		n := 0
		invoke := func(name string, args any, out any) bool {
			n++
			result := th.CallTool(context.Background(), turn, fmt.Sprintf("named-tool-%d", n), name, args)
			var body struct {
				OK     bool            `json:"ok"`
				Result json.RawMessage `json:"result"`
			}
			if !result.Success || json.Unmarshal([]byte(result.Text), &body) != nil || !body.OK {
				select {
				case failed <- name + ": " + result.Text:
				default:
				}
				return false
			}
			if out != nil && json.Unmarshal(body.Result, out) != nil {
				select {
				case failed <- "invalid typed " + name + ": " + string(body.Result):
				default:
				}
				return false
			}
			return true
		}
		reject := func(name string, args any, code protocol.Code) bool {
			n++
			result := th.CallTool(context.Background(), turn, fmt.Sprintf("named-tool-%d", n), name, args)
			var response struct {
				OK    bool            `json:"ok"`
				Error *protocol.Error `json:"error"`
			}
			if result.Success || json.Unmarshal([]byte(result.Text), &response) != nil || response.Error == nil || response.Error.Code != code {
				select {
				case failed <- "expected rejection " + name + ": " + result.Text:
				default:
				}
				return false
			}
			return true
		}
		manager := false
		for _, s := range th.Config.Tools {
			if s.Name == "issues_context_review" {
				manager = true
			}
		}
		if !manager {
			var state protocol.LogicalTurnState
			if !invoke("conversation_turn_read", map[string]any{}, &state) || len(state.Messages) == 0 {
				return "failed"
			}
			source := state.Messages[0]
			revision := source.ContentRevision
			p := protocol.CreateIssuePayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source.MessageID, ExpectedSourceRevision: &revision}, Plan: protocol.WorkItemPlan{Title: "Investigate the vague owner goal", Project: "default", Intent: "Determine what is feasible without executing directly"}, Assignee: &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/manager"}}
			if repairMode && in.Repair {
				if !reject("issues_create", map[string]any{"effect_seq": 1000, "payload": p}, protocol.CodeForbidden) {
					return "failed"
				}
				if len(readSources) > 1 && readSources[1] {
					for _, message := range state.Messages {
						ids := []string{}
						for _, item := range message.Items {
							ids = append(ids, item.WorkItemID)
						}
						disposition := "unassigned"
						if len(ids) > 0 {
							disposition = "linked"
						}
						if !invoke("conversation_associate_issues", map[string]any{"effect_seq": 1000 + int64(n), "target_id": message.MessageID, "expected_control_revision": message.Revision, "payload": map[string]any{"disposition": disposition, "content_revision": message.ContentRevision, "issue_ids": ids}}, nil) {
							return "failed"
						}
					}
					if !invoke("conversation_turn_read", map[string]any{}, &state) {
						return "failed"
					}
				}
				review := protocol.LogicalTurnCommit{Contexts: state.Contexts}
				for _, m := range state.Messages {
					review.Messages = append(review.Messages, protocol.LogicalTurnReview{MessageID: m.MessageID, ContentRevision: m.ContentRevision, AssociationRevision: m.Revision})
				}
				if !invoke("conversation_turn_commit", review, nil) {
					return "failed"
				}
				return "completed"
			}
			var created struct {
				Outcome protocol.Issue `json:"outcome"`
			}
			seq := int64(1)
			if ownerTransfer {
				own := p
				own.Plan.Title = "Owner retains concrete responsibility"
				own.Assignee = &protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}
				if !invoke("issues_create", map[string]any{"effect_seq": seq, "payload": own}, &created) {
					return "failed"
				}
				// The planner must finish its own owner delegation before any
				// subsequent agent-assigned Issue can classify this source.
				if !invoke("conversation_turn_read", map[string]any{}, &state) {
					return "failed"
				}
				review := protocol.LogicalTurnCommit{Contexts: state.Contexts}
				for _, message := range state.Messages {
					review.Messages = append(review.Messages, protocol.LogicalTurnReview{MessageID: message.MessageID, ContentRevision: message.ContentRevision, AssociationRevision: message.Revision})
				}
				if !invoke("conversation_turn_commit", review, nil) {
					return "failed"
				}
				seq++
				temporary := p
				temporary.Plan.Title = "Temporary agent responsibility before owner handoff"
				if !invoke("issues_create", map[string]any{"effect_seq": seq, "payload": temporary}, &created) {
					return "failed"
				}
				seq++
				assignment := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: created.Outcome.AssignmentEpoch, Assignee: *own.Assignee, Reason: "Transfer concrete responsibility to the human owner"}
				if !invoke("issues_assign", map[string]any{"effect_seq": seq, "target_id": created.Outcome.IssueID, "expected_control_revision": created.Outcome.Revision, "payload": assignment}, &created) {
					return "failed"
				}
				seq++
				finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: created.Outcome.AssignmentEpoch, HandoffID: created.Outcome.PendingHandoffID, Reason: "No old manager was admitted; enable the human owner"}
				if !invoke("issues_finish_assignment", map[string]any{"effect_seq": seq, "target_id": created.Outcome.IssueID, "expected_control_revision": created.Outcome.Revision, "payload": finish}, &created) {
					return "failed"
				}
				seq++
				if created.Outcome.Assignee.Ref != owner.Subject || created.Outcome.AssignmentState != "active" {
					failed <- "handoff did not preserve actual owner"
					return "failed"
				}
				own.Assignee = &protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: e.m.bindingInstance()}
				if !reject("issues_create", map[string]any{"effect_seq": seq, "payload": own}, protocol.CodeInvalidRequest) {
					return "failed"
				}
			}
			if !(len(readSources) > 1 && readSources[1]) {
				allowOnce.Do(func() { close(allowManagers) })
			}
			if !invoke("issues_create", map[string]any{"effect_seq": seq, "payload": p}, &created) {
				return "failed"
			}
			if len(readSources) > 1 && readSources[1] {
				var page protocol.ConversationPage
				if !invoke("conversation_read", map[string]any{}, &page) {
					return "failed"
				}
				var message protocol.ConversationItem
				for _, candidate := range page.Items {
					if candidate.MessageID == source.MessageID {
						message = candidate
					}
				}
				if message.Associations == nil {
					failed <- "source revisions missing"
					return "failed"
				}
				if !invoke("conversation_associate_issues", map[string]any{"effect_seq": seq + 1, "target_id": message.MessageID, "expected_control_revision": message.Associations.Revision, "payload": map[string]any{"disposition": "linked", "content_revision": message.Associations.ContentRevision, "issue_ids": []string{created.Outcome.IssueID}}}, nil) {
					return "failed"
				}
			}
			if len(readSources) > 1 && readSources[1] {
				allowOnce.Do(func() { close(allowManagers) })
			}
			if repairMode {
				return "completed"
			}
			if !invoke("conversation_turn_read", map[string]any{}, &state) {
				return "failed"
			}
			review := protocol.LogicalTurnCommit{Contexts: state.Contexts}
			for _, m := range state.Messages {
				review.Messages = append(review.Messages, protocol.LogicalTurnReview{MessageID: m.MessageID, ContentRevision: m.ContentRevision, AssociationRevision: m.Revision})
			}
			if !invoke("conversation_turn_commit", review, nil) {
				return "failed"
			}
			return "completed"
		}
		if th.Config.Sandbox != "read-only" || th.Config.ApprovalPolicy != "never" || th.Config.Config["features.shell_tool"] != false || th.Config.Config["features.apply_patch_freeform"] != false || strings.Contains(th.Config.DeveloperInstructions, "background checker") {
			select {
			case failed <- "actual manager retained native auto-mode or checker scope":
			default:
			}
			return "failed"
		}
		if taskControls {
			listed := map[string]bool{}
			for _, spec := range th.Config.Tools {
				listed[spec.Name] = true
			}
			if !listed["tasks_cancel"] || !listed["tasks_rate"] || !listed["actions_create"] || listed["tasks_set_control"] {
				failed <- "manager native tool registration does not match its task control role"
				return "failed"
			}
		}
		if len(readSources) > 0 && readSources[0] {
			var read struct {
				Outcome protocol.BackgroundSourcePage `json:"outcome"`
			}
			if !invoke("background_sources", map[string]any{}, &read) {
				return "failed"
			}
			if read.Outcome.Batch.WorkItemID == "" || len(read.Outcome.Sources) != 1 || read.Outcome.Sources[0].Kind != "issue.delegated" {
				failed <- "manager did not read its original exact delegated source"
				return "failed"
			}
		}
		var item protocol.IssueView
		if !invoke("issues_read", map[string]any{}, &item) {
			return "failed"
		}
		p := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: item.AssignmentEpoch, Title: "Investigate and return an independently checked report", Spec: protocol.OwnerTaskSpec{Goal: "Investigate the owner's unclear goal", Acceptance: []string{"Report identifies feasible choices and unknowns with evidence"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}
		var made struct {
			Outcome struct {
				TaskID string `json:"task_id"`
			} `json:"outcome"`
		}
		if managerRepair && in.Repair {
			if !reject("issues_create_task", map[string]any{"effect_seq": 1000, "expected_control_revision": item.Revision, "payload": p}, protocol.CodeForbidden) {
				return "failed"
			}
		} else {
			if !invoke("issues_create_task", map[string]any{"effect_seq": 1, "expected_control_revision": item.Revision, "payload": p}, &made) {
				return "failed"
			}
			if managerRepair {
				return "completed"
			}
		}
		seq := int64(2)
		if taskControls {
			var task protocol.TaskDetail
			read := func() bool { return invoke("tasks_read", map[string]any{"task_id": made.Outcome.TaskID}, &task) }
			if !read() {
				return "failed"
			}
			if !reject("tasks_cancel", map[string]any{"effect_seq": seq, "target_id": otherTask, "expected_control_revision": task.ControlRevision, "payload": map[string]any{}}, protocol.CodeForbidden) {
				return "failed"
			}
			if !reject("tasks_set_control", map[string]any{"task_id": task.TaskID, "expected_control_revision": task.ControlRevision, "mode": "human_control", "reason": "must remain owner-only"}, protocol.CodeForbidden) {
				return "failed"
			}
			if !reject("actions_create", map[string]any{"effect_seq": seq, "target_id": task.TaskID, "expected_control_revision": task.ControlRevision, "payload": map[string]any{"kind": "handback", "generation": 1}}, protocol.CodeInvalidRequest) {
				return "failed"
			}
			if !reject("actions_create", map[string]any{"effect_seq": seq, "target_id": task.TaskID, "expected_control_revision": task.ControlRevision, "payload": map[string]any{"kind": "human_control", "generation": 1}}, protocol.CodeInvalidRequest) {
				return "failed"
			}
			if !invoke("actions_create", map[string]any{"effect_seq": seq, "target_id": task.TaskID, "expected_control_revision": task.ControlRevision, "payload": protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: task.NextImplementGeneration}}, nil) {
				return "failed"
			}
			seq++
			if !read() {
				return "failed"
			}
			if !invoke("tasks_rate", map[string]any{"effect_seq": seq, "target_id": task.TaskID, "expected_control_revision": task.ControlRevision, "payload": protocol.RateTaskPayload{Rating: protocol.RatingP2}}, nil) {
				return "failed"
			}
			seq++
			if !read() {
				return "failed"
			}
			if !reject("tasks_cancel", map[string]any{"effect_seq": seq, "target_id": task.TaskID, "expected_control_revision": task.ControlRevision - 1, "payload": map[string]any{}}, protocol.CodeStaleRevision) {
				return "failed"
			}
			args := map[string]any{"effect_seq": seq, "target_id": task.TaskID, "expected_control_revision": task.ControlRevision, "payload": map[string]any{}}
			if !invoke("tasks_cancel", args, nil) {
				return "failed"
			}
			// Same native callback identity reads its receipt and does not add a second effect.
			result := th.CallTool(context.Background(), turn, fmt.Sprintf("named-tool-%d", n), "tasks_cancel", args)
			if !result.Success {
				failed <- "cancel receipt replay failed: " + result.Text
				return "failed"
			}
			seq++
			if !read() || task.Lifecycle != protocol.TaskCancelled || task.ControlMode == protocol.ControlHumanControl {
				failed <- "manager task controls bypassed lifecycle/owner-only mode"
				return "failed"
			}
		}
		if !invoke("issues_read", map[string]any{}, &item) {
			return "failed"
		}
		review := protocol.IssueContextReview{ExpectedAssignmentEpoch: item.AssignmentEpoch, ContextRevision: item.ContextRevision, Summary: "Vague intent retained; a worker investigation will establish feasibility"}
		for _, s := range item.SourceReviews {
			review.Sources = append(review.Sources, protocol.LogicalTurnReview{MessageID: s.MessageID, ContentRevision: s.ContentRevision, AssociationRevision: s.AssociationRevision})
		}
		if !invoke("issues_context_review", map[string]any{"effect_seq": seq, "payload": review}, nil) {
			return "failed"
		}
		var state protocol.LogicalTurnState
		if !invoke("issues_turn_read", map[string]any{}, &state) {
			return "failed"
		}
		commit := protocol.LogicalTurnCommit{Contexts: state.Contexts}
		for _, m := range state.Messages {
			commit.Messages = append(commit.Messages, protocol.LogicalTurnReview{MessageID: m.MessageID, ContentRevision: m.ContentRevision, AssociationRevision: m.Revision})
		}
		if !invoke("issues_turn_commit", map[string]any{"effect_seq": seq + 1, "payload": commit}, nil) {
			return "failed"
		}
		managerDone <- item.IssueID
		return "completed"
	}
	namedTurn := h.Turn
	e = newEnv(t, h)
	e.stop()
	if repairMode || managerRepair {
		h.Turn = namedTurn
	}
	e.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}}, Hosts: []app.HostConfig{{HostID: "worker-host", OS: "linux", Arch: "amd64", Slots: 1, TokenHash: strings.Repeat("ab", 32), Nodes: []string{"tag:test-worker"}}}}, app.SyncConfig{})
	e.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "worker-host"}, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "worker-host"}, protocol.HelloPayload{WorkspaceProtocol: 1}, app.Hello{})
	config := hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "NAMED_EXECUTABLE", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "manager-model", Effort: "high", Enabled: true}, {LocalID: "manager", Revision: 1, DisplayName: "Issue manager", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: "manager-model", Effort: "high", Enabled: true}}}}
	prepared, err := hostedconfig.Prepare(config)
	if err != nil {
		t.Fatal(err)
	}
	execute := func(principal protocol.Principal, op string, target protocol.Target, payload any, command app.Command) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		zero := int64(0)
		result, err := e.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: op, Target: target, ExpectedControlRevision: &zero, Payload: raw}, command)
		if err != nil || result.Status != protocol.OpSucceeded {
			t.Fatal(result, err)
		}
	}
	execute(owner, "categories.put", protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, app.PutAgentCategory{})
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "hosted.configure", protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, app.AcceptHostedConfiguration{Prepared: prepared})
	e.app = app.New(e.store, e.ids, e.now, app.WithNotifier(e.signal), app.WithHostedConfiguration(prepared))
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	service := tools.New(e.app, e.store, cb, quiet())
	makeManager := func(conversation string) *Manager {
		return New(e.app, cb, service, Config{Instance: "hosted-runtime/" + conversation, ConversationID: conversation, Thread: ThreadConfig(t.TempDir(), "UNUSED_LEGACY_MODEL"), Open: h.Open, OpenNamed: func(ctx context.Context, s hostedconfig.Selection) (harness.Session, error) {
			if s.Runtime.ExecutableRef != "NAMED_EXECUTABLE" {
				return nil, fmt.Errorf("wrong executable")
			}
			return h.Open(ctx)
		}, RestartDelay: 20 * time.Millisecond, Log: quiet()})
	}
	if taskControls {
		payload, _ := json.Marshal(protocol.CreateIssuePayload{Plan: protocol.WorkItemPlan{Title: "Separate owner's Issue", Project: "default", Intent: "Keep primary control separate"}})
		op, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "issues.create", Target: protocol.Target{Type: "issue"}, Payload: payload}, app.CanonicalOwnerIssueCommand{Action: "create"})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		var separate protocol.Issue
		if err := json.Unmarshal(op.Result, &separate); err != nil {
			t.Fatal(err)
		}
		taskPayload := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: separate.AssignmentEpoch, Title: "Separate primary task", Spec: protocol.OwnerTaskSpec{Goal: "Independent investigation", Acceptance: []string{"Report result"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}
		payload, _ = json.Marshal(taskPayload)
		op, err = e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "issues.create_task", Target: protocol.Target{Type: "issue", ID: separate.IssueID}, ExpectedControlRevision: &separate.Revision, Payload: payload}, app.CanonicalOwnerTaskCreate{})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		var task struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(op.Result, &task); err != nil {
			t.Fatal(err)
		}
		otherTask = task.TaskID
	}
	e.m = makeManager("")
	e.start()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pool := &BackgroundPool{App: e.app, Front: e.m, New: func(id string) (*Manager, error) {
		select {
		case <-allowManagers:
			return makeManager(id), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	done := make(chan struct{})
	go func() { defer close(done); pool.Run(ctx) }()
	defer func() { cancel(); <-done }()
	e.say("Investigate this unclear goal; the assistant should delegate the actual investigation")
	var issue string
	select {
	case issue = <-managerDone:
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("actual pooled manager did not complete its scoped tool chain")
	}
	select {
	case err := <-failed:
		t.Fatal(err)
	default:
	}
	eventually(t, "separate planner and manager logical turns settle", func() bool { return e.count(`SELECT COUNT(*) FROM logical_turns WHERE status='completed'`) == 2 })
	expectedTasks := 1
	if taskControls {
		expectedTasks = 2
	}
	if e.count(`SELECT COUNT(*) FROM tasks`) != expectedTasks || e.count(`SELECT COUNT(*) FROM task_work_item_owners WHERE work_item_id=?`, issue) != 1 || e.count(`SELECT COUNT(*) FROM runs`) != 0 {
		t.Fatal("coordination did not delegate exactly one independent worker task")
	}
	expectedManagerExecutions := 1
	if managerRepair {
		expectedManagerExecutions = 2
	}
	if e.count(`SELECT COUNT(*) FROM hosted_executions WHERE issue_id=? AND role='issue_manager'`, issue) != expectedManagerExecutions {
		t.Fatal("manager identity/scope was not frozen")
	}
	if e.count(`SELECT COUNT(*) FROM background_assessments`) != 0 {
		t.Fatal("actual manager was treated as a legacy checker")
	}
	select {
	case err := <-failed:
		t.Fatal(err)
	default:
	}
}
