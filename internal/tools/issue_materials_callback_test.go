package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestIssueMaterialsManagedCallbackReadsBackgroundOriginalWithoutMessage(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager} {
		t.Run(string(purpose), func(t *testing.T) {
			e := newEnv(t)
			clock := testkit.NewClock()
			ids := testkit.NewIDGen(clock, 110)
			owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test-owner"}
			execute := func(who protocol.Principal, command app.Command, target protocol.Target, payload any, expected *int64) protocol.Operation {
				t.Helper()
				raw, _ := json.Marshal(payload)
				op, err := e.svc.app.Execute(t.Context(), who, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: command.Operation(), Target: target, ExpectedControlRevision: expected, Payload: raw}, command)
				if err != nil || op.Status != protocol.OpSucceeded && op.Status != protocol.OpAccepted {
					t.Fatal(command.Operation(), op, err)
				}
				return op
			}
			execute(owner, app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, new(int64))
			prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1,
				Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "test-hosted", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}},
				Agents:   []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}, Model: "test-model", Effort: "high", Enabled: true}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			execute(protocol.Principal{Kind: protocol.PrincipalSystem}, app.AcceptHostedConfiguration{Prepared: prepared}, protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, new(int64))
			e.svc.app = app.New(e.store, ids, clock.Now, app.WithHostedConfiguration(prepared))
			content := "original retained background; no owner answer message exists"
			upload, err := e.svc.app.UploadConversationAttachment(t.Context(), owner, ids.New(protocol.PrefixRequest), "background.txt", "text/plain", strings.NewReader(content))
			if err != nil || upload.Status != protocol.OpSucceeded {
				t.Fatal(upload, err)
			}
			var file protocol.ConversationAttachment
			if err := json.Unmarshal(upload.Result, &file); err != nil {
				t.Fatal(err)
			}
			op := execute(owner, app.CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Read original context", Project: "default", Intent: "Use retained original evidence", Background: []protocol.SharedFact{{Kind: protocol.SharedFactInput, Text: "Retained original", Source: "file:" + file.FileID}}}}, nil)
			var issue protocol.Issue
			if err := json.Unmarshal(op.Result, &issue); err != nil {
				t.Fatal(err)
			}
			conversation, destination := "main", app.MainThreadDestination
			if purpose == agentconfig.IssueManager {
				op = execute(owner, app.HostedIssueAssignment{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/planner"}, Reason: "Read this Issue's retained context"}, &issue.Revision)
				_ = json.Unmarshal(op.Result, &issue)
				op = execute(owner, app.HostedIssueAssignment{Finish: true}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "No prior manager"}, &issue.Revision)
				_ = json.Unmarshal(op.Result, &issue)
				conversation = "issue:" + issue.IssueID
				destination = "background_thread:" + conversation
			}
			execute(owner, app.SubmitMessage{}, protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Read the retained Issue material"}, nil)
			delivery, _, ok, err := e.svc.app.NextDelivery(t.Context(), destination)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			selection, named, err := e.svc.app.HostedSelectionForAdmission(t.Context(), delivery.RequestID, conversation)
			if err != nil || !named {
				t.Fatal(named, err)
			}
			var input app.HandleMessage
			if err := json.Unmarshal(delivery.Payload, &input); err != nil {
				t.Fatal(err)
			}
			turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: input.MessageID, ThreadID: "materials-thread", Status: store.TurnStarting, Hosted: &selection, Model: selection.Definition.Model, Effort: selection.Definition.Effort}
			if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			turn.Status, turn.TurnID = store.TurnStarted, "materials-turn"
			if err := e.svc.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "named-main", Thread: turn.ThreadID}
			if purpose == agentconfig.IssueManager {
				principal.Kind, principal.Subject, principal.ConversationID = protocol.PrincipalBackgroundAgent, "named-manager", conversation
			}
			call := func(tool string, args any) answerView {
				t.Helper()
				raw, _ := json.Marshal(args)
				result := e.svc.Handle(t.Context(), Call{Principal: principal, Native: "original-" + tool, TurnID: turn.TurnID, ExecutionRequestID: turn.RequestID, ParentRequestID: delivery.RequestID, Tool: tool, Arguments: raw})
				var answer answerView
				if err := json.Unmarshal([]byte(result.Text), &answer); err != nil || !answer.OK {
					t.Fatal("normal managed read failed", tool, result.Text, err)
				}
				return answer
			}
			answer := call("issues_materials_read", map[string]any{"issue_id": issue.IssueID, "kind": "files", "limit": 1})
			var materials protocol.IssueMaterials
			if err := json.Unmarshal(answer.Result, &materials); err != nil || len(materials.Files) != 1 || materials.Files[0].FileID != file.FileID || materials.Files[0].SHA256 != file.SHA256 || len(materials.Files[0].Sources) == 0 || materials.Files[0].Sources[0].MessageID != "" {
				t.Fatal("background original gained a fabricated message or changed identity", materials, err)
			}
			if purpose == agentconfig.IssueManager {
				answer := call("background_evidence_read", evidenceArgs{FileID: file.FileID})
				var window issueMaterialWindow
				if err := json.Unmarshal(answer.Result, &window); err != nil || window.Text != content || window.FileID != file.FileID || window.SHA256 != file.SHA256 || !window.EOF {
					t.Fatal("retained bytes required an owner answer message", window, err)
				}
			}
			if n := e.tasks(); n != 0 {
				t.Fatal("ordinary reading scheduled a Task", n)
			}
		})
	}
}
