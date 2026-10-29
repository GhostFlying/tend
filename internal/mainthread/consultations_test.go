package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func TestConsultationActualHostedPoolDelegatesOnlyExecutionRequest(t *testing.T) {
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "consult-model", SupportedEfforts: []string{"high"}, DefaultEffort: "high"}}
	done := make(chan string, 8)
	failures := make(chan string, 8)
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		fail := func(s string) string {
			select {
			case failures <- s:
			default:
			}
			return "failed"
		}
		if th.Config.Sandbox != "read-only" || th.Config.Config["features.shell_tool"] != false || th.Config.Config["features.apply_patch_freeform"] != false {
			return fail("hosted consultation/manager has native execution")
		}
		n := 0
		invoke := func(name string, args any, out any) bool {
			n++
			v := th.CallTool(context.Background(), turn, fmt.Sprintf("consult-tool-%d", n), name, args)
			var body struct {
				OK     bool            `json:"ok"`
				Result json.RawMessage `json:"result"`
			}
			if !v.Success || json.Unmarshal([]byte(v.Text), &body) != nil || !body.OK {
				fail(name + ": " + v.Text)
				return false
			}
			if out != nil && json.Unmarshal(body.Result, out) != nil {
				fail("invalid typed result " + name)
				return false
			}
			return true
		}
		consultation := false
		for _, tool := range th.Config.Tools {
			if tool.Name == "consultation_promote" {
				consultation = true
			}
		}
		if consultation {
			for _, tool := range th.Config.Tools {
				if tool.Name == "issues_create_task" || tool.Name == "runs_send_message" || tool.Name == "tasks_create" || tool.Name == "agent_categories_list" || tool.Name == "agents_runner_read" {
					return fail("consultation advertised generic mutation")
				}
			}
			var page protocol.ConversationPage
			if !invoke("conversation_read", map[string]any{}, &page) {
				return "failed"
			}
			var source protocol.ConversationItem
			for _, m := range page.Items {
				if m.Author == "owner" && m.Seq > source.Seq {
					source = m
				}
			}
			if source.MessageID == "" || source.Associations == nil {
				return fail("actual consultation cannot read its own versioned owner input")
			}
			if !strings.Contains(source.Body, "Please execute") {
				done <- "question"
				return "completed"
			}
			p := protocol.PromoteConsultationPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source.MessageID, ExpectedSourceRevision: &source.Associations.ContentRevision}, Plan: protocol.WorkItemPlan{Title: "Delegated consultation goal", Project: "default", Intent: "Return a reviewed report"}, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/adviser"}}
			args := map[string]any{"effect_seq": 1, "payload": p}
			if !invoke("consultation_promote", args, nil) {
				return "failed"
			}
			// A second native call ID cannot produce the same durable effect again.
			repeated := th.CallTool(context.Background(), turn, "another-native-call", "consultation_promote", args)
			if repeated.Success || !strings.Contains(repeated.Text, string(protocol.CodeDuplicateEffect)) {
				return fail("consultation repeated business effect")
			}
			done <- "promoted"
			return "completed"
		}
		var issue protocol.IssueView
		if !invoke("issues_read", map[string]any{}, &issue) {
			return "failed"
		}
		p := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Investigate delegated question", Spec: protocol.OwnerTaskSpec{Goal: "Return an evidence-backed investigation", Acceptance: []string{"Report identifies known facts and uncertainty"}, Deliverables: []string{"report"}, Workspace: protocol.OwnerTaskWorkspace{Mode: "temporary"}}}
		if !invoke("issues_create_task", map[string]any{"effect_seq": 1, "expected_control_revision": issue.Revision, "payload": p}, nil) {
			return "failed"
		}
		if !invoke("issues_read", map[string]any{}, &issue) {
			return "failed"
		}
		review := protocol.IssueContextReview{ExpectedAssignmentEpoch: issue.AssignmentEpoch, ContextRevision: issue.ContextRevision, Summary: "Reviewed selected consultation sources"}
		for _, s := range issue.SourceReviews {
			review.Sources = append(review.Sources, protocol.LogicalTurnReview{MessageID: s.MessageID, ContentRevision: s.ContentRevision, AssociationRevision: s.AssociationRevision})
		}
		if !invoke("issues_context_review", map[string]any{"effect_seq": 2, "payload": review}, nil) {
			return "failed"
		}
		var logical protocol.LogicalTurnState
		if !invoke("issues_turn_read", map[string]any{}, &logical) {
			return "failed"
		}
		commit := protocol.LogicalTurnCommit{Contexts: logical.Contexts}
		for _, m := range logical.Messages {
			commit.Messages = append(commit.Messages, protocol.LogicalTurnReview{MessageID: m.MessageID, ContentRevision: m.ContentRevision, AssociationRevision: m.Revision})
		}
		if !invoke("issues_turn_commit", map[string]any{"effect_seq": 3, "payload": commit}, nil) {
			return "failed"
		}
		done <- "task"
		return "completed"
	}
	e := newEnv(t, h)
	e.stop()
	e.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}}, Hosts: []app.HostConfig{{HostID: "worker-host", OS: "linux", Arch: "amd64", Slots: 1, TokenHash: strings.Repeat("ab", 32), Nodes: []string{"tag:worker"}}}}, app.SyncConfig{})
	e.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "worker-host"}, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "worker-host"}, protocol.HelloPayload{WorkspaceProtocol: 1}, app.Hello{})
	config := hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "adviser", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "HOSTED_EXECUTABLE", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "adviser", Revision: 1, DisplayName: "Adviser", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation}, Model: "consult-model", Effort: "high", Enabled: true}}}}
	prepared, err := hostedconfig.Prepare(config)
	if err != nil {
		t.Fatal(err)
	}
	execute := func(operation string, target protocol.Target, p any, cmd app.Command, principal protocol.Principal) protocol.Operation {
		t.Helper()
		raw, _ := json.Marshal(p)
		zero := int64(0)
		op, err := e.app.Execute(t.Context(), principal, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw, ExpectedControlRevision: &zero}, cmd)
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
		return op
	}
	execute("categories.put", protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"}, app.PutAgentCategory{}, owner)
	execute("hosted.configure", protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, app.AcceptHostedConfiguration{Prepared: prepared}, protocol.Principal{Kind: protocol.PrincipalSystem})
	e.app = app.New(e.store, e.ids, e.now, app.WithHostedConfiguration(prepared), app.WithNotifier(e.signal))
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	service := tools.New(e.app, e.store, cb, quiet())
	makeManager := func(id string) *Manager {
		return New(e.app, cb, service, Config{Instance: "hosted/" + id, ConversationID: id, Thread: ThreadConfig(t.TempDir(), "UNUSED"), Open: h.Open, OpenNamed: func(ctx context.Context, selection hostedconfig.Selection) (harness.Session, error) {
			return h.Open(ctx)
		}, RestartDelay: 10 * time.Millisecond, Log: quiet()})
	}
	// The existing pool is the only consultation lifecycle. No front main is needed.
	front := makeManager("main")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pool := &BackgroundPool{App: e.app, Front: front, New: func(id string) (*Manager, error) { return makeManager(id), nil }}
	stopped := make(chan struct{})
	go func() { defer close(stopped); pool.Run(ctx) }()
	defer func() { cancel(); <-stopped }()
	created := execute("conversation.consult", protocol.Target{Type: "conversation"}, protocol.CreateConsultationPayload{AgentRef: "hosted/adviser", Title: "Ordinary advisory conversation"}, app.CreateConsultation{}, owner)
	var consultation protocol.AgentConversation
	if err := json.Unmarshal(created.Result, &consultation); err != nil {
		t.Fatal(err)
	}
	send := func(text string) {
		execute("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: consultation.ID, Body: text}, app.SubmitMessage{}, owner)
	}
	wait := func(want string) {
		t.Helper()
		timeout := time.NewTimer(10 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case got := <-done:
				if got == want {
					return
				}
			case err := <-failures:
				t.Fatal(err)
			case <-timeout.C:
				t.Fatal("waiting for", want)
			}
		}
	}
	send("Explain the tradeoffs without doing any work")
	wait("question")
	eventually(t, "question native receipt settled", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE destination=? AND status='sending'`, "background_thread:"+consultation.ID) == 0
	})
	if e.count(`SELECT COUNT(*) FROM issue_controls`) > 0 || e.count(`SELECT COUNT(*) FROM tasks`) > 0 || e.count(`SELECT COUNT(*) FROM runs`) > 0 {
		t.Fatal("Q&A created execution")
	}
	send("Please execute an investigation report for this goal")
	wait("task")
	if e.count(`SELECT COUNT(*) FROM issue_controls`) != 1 || e.count(`SELECT COUNT(*) FROM tasks`) != 1 {
		t.Fatal("actual callback chain did not create exactly one delegated task")
	}
}
