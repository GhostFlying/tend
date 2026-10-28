package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func sampleDecisionIssue() protocol.DecisionIssue {
	return protocol.DecisionIssue{IssueID: "scratch", Category: "environment", Summary: "scratch unavailable", Impact: "temporary plan cannot be saved; evidence reads still work", NextAction: "repair worker preparation", Actor: "main_agent", Status: "workaround", Source: "worker command result"}
}

func TestWorkerIssuesAndCoordinatorBriefing(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	p := scopeQuestion
	p.Issues = []protocol.DecisionIssue{sampleDecisionIssue()}
	q := askQuestion(t, h, as, p)
	if len(q.Issues) != 1 {
		t.Fatal(q)
	}
	// A new transport identity still replays the original question and issues.
	if again := askQuestion(t, h, as, p); again.DecisionID != q.DecisionID {
		t.Fatal(again)
	}
	var original store.DecisionRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		original, err = store.GetDecision(t.Context(), tx.Q(), q.DecisionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	brief := protocol.ExplainDecisionPayload{DecisionVersion: q.Version, Summary: "Local preparation is impaired separately from external evidence access", OwnerChoice: "Choose the report's evidence boundary", Issues: p.Issues, SourceMessageID: original.AskedMessageID}
	revision := int64(0)
	env := h.env("decisions.explain", protocol.Target{Type: "decision", ID: q.DecisionID}, brief, &revision)
	if op := h.exec(mainAgent, env, ExplainDecision{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.exec(mainAgent, env, ExplainDecision{}); op.Status != protocol.OpSucceeded || h.count("decision_briefings") != 1 {
		t.Fatal(op)
	}
	if op := h.exec(mainAgent, h.env("decisions.explain", env.Target, brief, &revision), ExplainDecision{}); op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(op)
	}
	if _, err := h.app.Execute(t.Context(), owner, h.env("decisions.explain", env.Target, brief, &revision), ExplainDecision{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
	revision = 1
	brief.Issues = nil
	if op := h.exec(mainAgent, h.env("decisions.explain", env.Target, brief, &revision), ExplainDecision{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal(op)
	}
	brief.Issues = p.Issues
	brief.Issues[0].Status = "resolved"
	if op := h.exec(mainAgent, h.env("decisions.explain", env.Target, brief, &revision), ExplainDecision{}); op.Status != protocol.OpSucceeded || h.count("decision_briefings") != 2 {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		d, err := store.GetDecision(t.Context(), tx.Q(), q.DecisionID)
		if err == nil && (d.Version != q.Version || d.Question != q.Question || d.Status != "open" || d.Briefing.Revision != 2 || d.Worker.Issues[0].Status != "workaround") {
			t.Fatalf("explanation altered original decision: %+v", d)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Owner still answers the original question version, not briefing revision 2.
	if op := answerQuestion(t, h, q, q.Options[0], ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestLegacyQuestionHashDoesNotGainExtension(t *testing.T) {
	raw, err := json.Marshal(scopeQuestion)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if _, exists := fields["issues"]; exists {
		t.Fatal("legacy replay identity changed")
	}
}

func TestCancelledStructuredQuestionStillReceivesStopVerdict(t *testing.T) {
	h, as, runtime := questionRun(t, protocol.RoleImplement)
	p := scopeQuestion
	p.Issues = []protocol.DecisionIssue{sampleDecisionIssue()}
	askQuestion(t, h, as, p)
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(as.TaskID), map[string]any{}, rev(h.task(as.TaskID).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old := *runtime
	old.InstanceID = "older-question-runner"
	old.Generation++
	old.Sequence = 1
	env := h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: &old, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}}, nil)
	op, err := h.app.Execute(t.Context(), runner("linux-1"), env, Hello{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("cancelled run reconciliation: %+v, %v", op, err)
	}
	var result protocol.HelloResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 1 || result.Runs[0].Verdict != protocol.VerdictStop {
		t.Fatalf("stop verdict lost: %+v", result)
	}
}
