package mainthread

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

func TestIssueStartupMigratesToolsetWithIdleSummariesDisabled(t *testing.T) {
	f := newHandoffFixture(t)
	m := f.e.m
	m.cfg.MaintenanceEnabled = false
	own := registeredHandoffIssue(t, f, "Current Issue")
	m.cfg.ConversationID = "issue:" + own.IssueID
	m.cfg.Instance = m.cfg.ConversationID
	m.named = &hostedconfig.Selection{Purpose: agentconfig.IssueManager, IssueID: own.IssueID}
	instance := m.bindingInstance()
	if _, err := f.e.app.BindMainThread(t.Context(), instance, f.source.ID(), "old-issue-tools", rolloutPath(f.source)); err != nil {
		t.Fatal(err)
	}
	snap, err := f.e.app.HandoffSnapshot(t.Context(), instance)
	if err != nil {
		t.Fatal(err)
	}
	snap.Summary = &store.SummaryCoverage{Complete: true, Method: "single", Batches: 1}
	if _, err := f.e.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: instance, Snapshot: snap, Body: "retained issue context", FormatVersion: summaryFormatVersion}); err != nil {
		t.Fatal(err)
	}
	target, err := m.openThread(t.Context(), f.session)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID() == f.source.ID() {
		t.Fatal("Issue resumed obsolete dynamic tools because idle summaries are disabled")
	}
	fake := target.(*handoffTestThread).Thread.(*testkit.FakeThread)
	names := map[string]bool{}
	for _, tool := range fake.Config.Tools {
		names[tool.Name] = true
	}
	if !names["issues_review_delivery"] || !names["issues_delivery_reviews_read"] {
		t.Fatal("current Issue tool catalog missing", names)
	}
	_, forks, _ := f.session.counts()
	if forks != 0 || m.cfg.MaintenanceEnabled {
		t.Fatal("Issue tool migration enabled model summaries", forks)
	}
}

func TestIssueCurrentToolsetResumesWithoutOptionalNativeHistoryIndex(t *testing.T) {
	f := newHandoffFixture(t)
	m := f.e.m
	m.cfg.MaintenanceEnabled = false
	own := registeredHandoffIssue(t, f, "Current Issue")
	m.cfg.ConversationID = "issue:" + own.IssueID
	m.cfg.Instance = m.cfg.ConversationID
	m.named = &hostedconfig.Selection{Purpose: agentconfig.IssueManager, IssueID: own.IssueID}
	hash, err := m.currentToolsetHash()
	if err != nil {
		t.Fatal(err)
	}
	instance := m.bindingInstance()
	if _, err := f.e.app.BindMainThread(t.Context(), instance, "original-issue-thread", hash, "retained-history-index"); err != nil {
		t.Fatal(err)
	}
	original := &receiptThread{Thread: &historyReference{id: "original-issue-thread"}}
	session := &receiptOnlySession{thread: original}
	resumed, err := m.openThread(t.Context(), session)
	if err != nil || resumed != original || session.resumes != 1 || session.starts != 0 {
		t.Fatal("a missing optional native history index blocked a valid current-toolset resume", resumed, err, session.resumes, session.starts)
	}
	binding, ok, err := f.e.app.MainThreadBinding(t.Context(), instance)
	if err != nil || !ok || binding.RolloutPath != "retained-history-index" || binding.ToolsetHash != hash || binding.ThreadID != original.ID() || binding.Generation != 1 {
		t.Fatal("missing index erased history or changed original binding", binding, err)
	}
}

func TestIssueHandoffDoesNotReadGlobalFactsOrSummaries(t *testing.T) {
	f := newHandoffFixture(t)
	own := registeredHandoffIssue(t, f, "OWN_ISSUE_SCOPE_MARKER")
	sibling := registeredHandoffIssue(t, f, "SIBLING_PRIVATE_SCOPE_MARKER")
	if _, err := f.e.app.AgentConversation(t.Context(), "issue:"+own.IssueID); err != nil {
		t.Fatal("test requires an actually registered Issue conversation", err)
	}
	f.e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + sibling.IssueID, Body: "SIBLING_PRIVATE_MESSAGE_MARKER"}, app.SubmitMessage{})
	m := f.e.m
	m.cfg.MaintenanceEnabled = false
	m.cfg.ConversationID = "issue:" + own.IssueID
	m.cfg.Instance = m.cfg.ConversationID
	m.named = &hostedconfig.Selection{Purpose: agentconfig.IssueManager, IssueID: own.IssueID}
	instance := m.bindingInstance()
	if _, err := f.e.app.BindMainThread(t.Context(), instance, f.source.ID(), "old-issue-tools", rolloutPath(f.source)); err != nil {
		t.Fatal(err)
	}
	snap, err := f.e.app.HandoffSnapshot(t.Context(), instance)
	if err != nil {
		t.Fatal(err)
	}
	snap.Summary = &store.SummaryCoverage{Complete: true, Method: "single", Batches: 1}
	// A cache from the old global input cannot become Issue authority merely
	// because its instance hash matches this native binding.
	if _, err := f.e.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: instance, Snapshot: snap, Body: "GLOBAL_CACHE_PRIVATE_MARKER", FormatVersion: summaryFormatVersion}); err != nil {
		t.Fatal(err)
	}
	prompt, err := m.handoffPrompt(t.Context(), f.source)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SIBLING_PRIVATE_SCOPE_MARKER", "SIBLING_PRIVATE_MESSAGE_MARKER", "GLOBAL_CACHE_PRIVATE_MARKER", sibling.IssueID} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("Issue handoff exposed out-of-scope %s", forbidden)
		}
	}
	if strings.Contains(prompt, "OWN_ISSUE_SCOPE_MARKER") || !strings.Contains(prompt, own.IssueID) || !strings.Contains(prompt, "index-v1") {
		t.Fatal("Issue handoff omitted its current facts", prompt)
	}
	before := f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts`)
	if _, err := m.openThread(t.Context(), f.session); err != nil {
		t.Fatal(err)
	}
	_, forks, _ := f.session.counts()
	if forks != 0 || f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts`) != before {
		t.Fatal("normal Issue handoff admitted global summary preparation", forks)
	}
}

func registeredHandoffIssue(t *testing.T, f *handoffFixture, title string) protocol.Issue {
	t.Helper()
	if _, named, err := f.e.app.NamedHostedSelection(t.Context(), "main"); err != nil || !named {
		execute := func(command app.Command, target protocol.Target, payload any) {
			t.Helper()
			raw, _ := json.Marshal(payload)
			zero := int64(0)
			op, err := f.e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: f.e.ids.New(protocol.PrefixRequest), Operation: command.Operation(), Target: target, ExpectedControlRevision: &zero, Payload: raw}, command)
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal(op, err)
			}
		}
		execute(app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "planning"}, protocol.PutAgentCategoryPayload{Title: "Planning", State: "active"})
		prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "hosted-executable", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager}, Model: "test-model", Effort: "high", Enabled: true}}}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()})
		zero := int64(0)
		op, err := f.e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: f.e.ids.New(protocol.PrefixRequest), Operation: "hosted.configure", Target: protocol.Target{Type: "hosted_configuration"}, ExpectedControlRevision: &zero, Payload: raw}, app.AcceptHostedConfiguration{Prepared: prepared})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		f.e.app = app.New(f.e.store, f.e.ids, f.e.now, app.WithHostedConfiguration(prepared))
		f.e.m.app = f.e.app
	}
	raw := f.e.exec(owner, "issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: title, Project: "default", Intent: "Preserve only this Issue's private goal"}, Assignee: &protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/planner"}}, app.CanonicalOwnerIssueCommand{Action: "create"})
	var i protocol.Issue
	if err := json.Unmarshal(raw, &i); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.app.AgentConversation(t.Context(), "issue:"+i.IssueID); err != nil {
		t.Fatal("real named assignment did not register the conversation", err)
	}
	return i
}
