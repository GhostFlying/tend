package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestNamedManagerReadsDescendantResultsWithoutControl(t *testing.T) {
	for _, spec := range NamedSpecs(agentconfig.IssueManager) {
		if spec.Name == "work_item_context_read" {
			t.Fatal("named manager still advertises retired legacy context facade")
		}
	}
	e := newEnv(t)
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 91)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test-owner"}
	execute := func(principal protocol.Principal, command app.Command, target protocol.Target, payload any, expected *int64) protocol.Operation {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		op, err := e.svc.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: command.Operation(), Target: target, ExpectedControlRevision: expected, Payload: raw}, command)
		if err != nil || op.Status != protocol.OpSucceeded && op.Status != protocol.OpAccepted {
			t.Fatal(op, err)
		}
		return op
	}
	execute(owner, app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, new(int64(0)))
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "test-hosted", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}, Model: "test-model", Effort: "high", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	execute(protocol.Principal{Kind: protocol.PrincipalSystem}, app.AcceptHostedConfiguration{Prepared: prepared}, protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, new(int64(0)))
	e.svc.app = app.New(e.store, ids, clock.Now, app.WithHostedConfiguration(prepared))
	execute(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, app.Hello{}, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1}, nil)
	create := func(parent string) protocol.Issue {
		t.Helper()
		op := execute(owner, app.CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{ParentIssueID: parent, Plan: protocol.IssuePlan{Title: "Investigation", Project: "default", Intent: "Review exact delegated result"}}, nil)
		var issue protocol.Issue
		if err := json.Unmarshal(op.Result, &issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	ancestor := create("")
	parent := create(ancestor.IssueID)
	child := create(parent.IssueID)
	descendant := create(child.IssueID)
	sibling := create(ancestor.IssueID)
	current := func(id string) protocol.Issue {
		t.Helper()
		var issue protocol.Issue
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			issue, err = store.GetCanonicalIssue(t.Context(), tx.Q(), id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	task := func(issue protocol.Issue) (string, string) {
		t.Helper()
		issue = current(issue.IssueID)
		op := execute(owner, app.CanonicalOwnerTaskCreate{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Report", Spec: protocol.OwnerTaskSpec{Goal: "Investigate", Acceptance: []string{"Report cites evidence"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}, &issue.Revision)
		var created struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(op.Result, &created); err != nil || created.TaskID == "" {
			t.Fatal(op, err)
		}
		run, artifact := ids.New(protocol.PrefixRun), ids.New(protocol.PrefixArtifact)
		// Persisted run/artifact facts are bounded read fixtures, not a claim that
		// this test exercised worker execution or independent acceptance.
		if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
			actions, err := store.ListActions(t.Context(), tx.Q(), created.TaskID)
			if err != nil {
				return err
			}
			if len(actions) == 0 {
				return fmt.Errorf("missing implement action")
			}
			if err := tx.InsertRun(t.Context(), store.RunRow{RunID: run, TaskID: created.TaskID, ActionID: actions[0].ActionID, Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunFinished}); err != nil {
				return err
			}
			return tx.InsertArtifact(t.Context(), artifact, created.TaskID, run, "report", `{"report":"exact delegated report","files":[]}`, "", clock.Now().UnixMilli())
		}); err != nil {
			t.Fatal(err)
		}
		return created.TaskID, artifact
	}
	type result struct {
		task, artifact, issue string
		allowed               bool
	}
	cases := map[string]result{}
	for name, issue := range map[string]protocol.Issue{"own": parent, "child": child, "descendant": descendant, "ancestor": ancestor, "sibling": sibling} {
		task, artifact := task(issue)
		cases[name] = result{task, artifact, issue.IssueID, name != "ancestor" && name != "sibling"}
	}
	parent = current(parent.IssueID)
	op := execute(owner, app.HostedIssueAssignment{}, protocol.Target{Type: "issue", ID: parent.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: parent.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/planner"}, Reason: "Review delegated results"}, &parent.Revision)
	if err := json.Unmarshal(op.Result, &parent); err != nil {
		t.Fatal(err)
	}
	op = execute(owner, app.HostedIssueAssignment{Finish: true}, protocol.Target{Type: "issue", ID: parent.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: parent.AssignmentEpoch, HandoffID: parent.PendingHandoffID, Reason: "No previous manager"}, &parent.Revision)
	if err := json.Unmarshal(op.Result, &parent); err != nil {
		t.Fatal(err)
	}
	conversation := "issue:" + parent.IssueID
	execute(owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Read the delegated reports"}, nil)
	delivery, _, ok, err := e.svc.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	selection, named, err := e.svc.app.HostedSelectionForAdmission(t.Context(), delivery.RequestID, conversation)
	if err != nil || !named {
		t.Fatal(named, err)
	}
	var payload app.HandleMessage
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: payload.MessageID, ThreadID: "native-parent", Status: store.TurnStarting, Hosted: &selection, Model: selection.Definition.Model, Effort: selection.Definition.Effort}
	if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.TurnID = store.TurnStarted, "parent-turn"
	if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "hosted-parent", Thread: turn.ThreadID, ConversationID: conversation}
	call := func(tool string, args any) answerView {
		t.Helper()
		raw, _ := json.Marshal(args)
		response := e.svc.Handle(t.Context(), Call{Principal: principal, Native: turn.ThreadID + "/" + turn.TurnID + "/" + ids.New(protocol.PrefixRequest), TurnID: turn.TurnID, ExecutionRequestID: turn.RequestID, ParentRequestID: delivery.RequestID, Tool: tool, Arguments: raw})
		var answer answerView
		if err := json.Unmarshal([]byte(response.Text), &answer); err != nil {
			t.Fatal(response.Text, err)
		}
		return answer
	}
	legacyContext := call("work_item_context_read", contextReadArgs{WorkItemID: parent.IssueID})
	if legacyContext.OK || legacyContext.Error == nil || legacyContext.Error.Code != protocol.CodeForbidden {
		t.Fatal("named manager still accepts retired legacy context facade", legacyContext)
	}
	a := call("issues_list", map[string]any{"limit": 2})
	var page protocol.IssuePage
	if !a.OK || json.Unmarshal(a.Result, &page) != nil || len(page.Items) != 2 || !page.More {
		t.Fatal("manager scoped discovery", a)
	}
	for _, item := range page.Items {
		if item.IssueID != parent.IssueID && item.IssueID != child.IssueID && item.IssueID != descendant.IssueID {
			t.Fatal("unrelated Issue in scoped discovery", item)
		}
	}
	queued := execute(owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "QUEUED_OWNER_MATERIAL_MUST_NOT_LEAK"}, nil)
	var accepted struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(queued.Result, &accepted); err != nil {
		t.Fatal(err)
	}
	projection := call("issues_read", map[string]any{"issue_id": parent.IssueID})
	if !projection.OK || strings.Contains(string(projection.Result), "QUEUED_OWNER_MATERIAL_MUST_NOT_LEAK") || strings.Contains(string(projection.Result), accepted.MessageID) {
		t.Fatal("agent Issue projection exposed queued owner material", projection)
	}
	contextProjection := call("issues_context_read", map[string]any{"issue_id": parent.IssueID})
	if !contextProjection.OK || strings.Contains(string(contextProjection.Result), "QUEUED_OWNER_MATERIAL_MUST_NOT_LEAK") {
		t.Fatal("canonical context bypassed shared source projection", contextProjection)
	}
	t.Run("named-main-preview", func(t *testing.T) {
		// The owner/store view keeps the original queued body. The named main
		// catalog is an agent input projection, not an owner history reader.
		var original protocol.AgentConversationPage
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			original, err = store.AgentConversations(t.Context(), tx.Q(), "", "", "", 30)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(original)
		if !strings.Contains(string(raw), "QUEUED_OWNER_MATERIAL_MUST_NOT_LEAK") {
			t.Fatal("fixture has no queued preview", string(raw))
		}
		execute(owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "List my issue conversations"}, nil)
		d, _, ok, err := e.svc.app.NextDelivery(t.Context(), app.MainThreadDestination)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		selected, named, err := e.svc.app.HostedSelectionForAdmission(t.Context(), d.RequestID, "main")
		if err != nil || !named {
			t.Fatal(named, err)
		}
		var input app.HandleMessage
		if err := json.Unmarshal(d.Payload, &input); err != nil {
			t.Fatal(err)
		}
		main := store.MainTurn{RequestID: d.RequestID, MessageID: input.MessageID, ThreadID: "native-main-preview", Status: store.TurnStarting, Hosted: &selected, Model: selected.Definition.Model, Effort: selected.Definition.Effort}
		if err := e.svc.app.PutMainTurn(t.Context(), main); err != nil {
			t.Fatal(err)
		}
		main.Status, main.TurnID = store.TurnStarted, "main-preview-turn"
		if err := e.svc.app.PutMainTurn(t.Context(), main); err != nil {
			t.Fatal(err)
		}
		response := e.svc.Handle(t.Context(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "named-main", Thread: main.ThreadID}, Native: main.ThreadID + "/" + main.TurnID + "/preview", TurnID: main.TurnID, ExecutionRequestID: main.RequestID, ParentRequestID: d.RequestID, Tool: "agent_conversations_list", Arguments: json.RawMessage(`{}`)})
		var answer answerView
		if err := json.Unmarshal([]byte(response.Text), &answer); err != nil {
			t.Fatal(response.Text, err)
		}
		if !answer.OK || strings.Contains(string(answer.Result), "QUEUED_OWNER_MATERIAL_MUST_NOT_LEAK") {
			t.Fatal("named main received unadmitted preview", response.Text)
		}
		var control protocol.MessageControl
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			control, err = store.MessageControl(t.Context(), tx.Q(), accepted.MessageID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		execute(owner, app.WithdrawMessage{}, protocol.Target{Type: "message", ID: accepted.MessageID}, protocol.WithdrawMessagePayload{}, &control.Revision)
		response = e.svc.Handle(t.Context(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "named-main", Thread: main.ThreadID}, Native: main.ThreadID + "/" + main.TurnID + "/withdrawn-preview", TurnID: main.TurnID, ExecutionRequestID: main.RequestID, ParentRequestID: d.RequestID, Tool: "agent_conversations_list", Arguments: json.RawMessage(`{}`)})
		if err := json.Unmarshal([]byte(response.Text), &answer); err != nil {
			t.Fatal(err)
		}
		if !answer.OK || strings.Contains(string(answer.Result), "QUEUED_OWNER_MATERIAL_MUST_NOT_LEAK") {
			t.Fatal("withdrawn preview became agent input", response.Text)
		}
		contextProjection := call("issues_context_read", map[string]any{"issue_id": parent.IssueID})
		if !contextProjection.OK || strings.Contains(string(contextProjection.Result), "QUEUED_OWNER_MATERIAL_MUST_NOT_LEAK") {
			t.Fatal("withdrawn source became canonical context input", contextProjection)
		}
	})
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			for _, read := range []struct {
				tool string
				args any
			}{{"tasks_read", taskArgs{TaskID: r.task}}, {"artifacts_read", artifactArgs{ArtifactID: r.artifact}}, {"background_evidence_read", evidenceArgs{ArtifactID: r.artifact}}, {"issues_context_read", map[string]any{"issue_id": r.issue}}, {"issues_followups_list", map[string]any{"issue_id": r.issue}}, {"issues_deliveries_read", map[string]any{"issue_id": r.issue}}} {
				a := call(read.tool, read.args)
				if r.allowed {
					if !a.OK {
						t.Errorf("%s lost scoped result: %+v", read.tool, a.Error)
					}
					if strings.Contains(read.tool, "evidence") && !strings.Contains(string(a.Result), "exact delegated report") {
						t.Error("missing original report", string(a.Result))
					}
				} else if a.OK || a.Error == nil || a.Error.Code != protocol.CodeForbidden {
					t.Errorf("%s admitted unrelated result: %+v", read.tool, a)
				}
			}
			if name != "own" {
				a := call("tasks_cancel", map[string]any{"target_id": r.task, "effect_seq": 1, "expected_control_revision": 1, "payload": map[string]any{}})
				if a.OK || a.Error == nil || a.Error.Code != protocol.CodeForbidden {
					t.Error("read scope granted task control", a)
				}
			}
		})
	}
}
