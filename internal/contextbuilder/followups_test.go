package contextbuilder

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/store"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFollowupRegressionCancelledFollowupFullContext(t *testing.T) {
	e := newEnv(t)
	app.WithFollowups(true)(e.app)
	var w protocol.WorkItem
	raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{IssuePlan: protocol.IssuePlan{Title: "Check", Intent: "Existing goal"}}, app.CreateIssue{})
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	var r protocol.FollowupRule
	cmd := app.FollowupCommand{Action: "create"}
	raw = e.exec(owner, cmd.Operation(), protocol.Target{Type: "work_item", ID: w.WorkItemID}, &w.Revision, protocol.FollowupSpec{Purpose: "check", Instruction: "CANCELLED_FOLLOWUP_INSTRUCTION", Kind: "at", At: testkit.Epoch.UnixMilli(), Timezone: "UTC"}, cmd)
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	cmd = app.FollowupCommand{Action: "cancel"}
	env := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: protocol.Target{Type: "followup", ID: r.RuleID}, ExpectedControlRevision: &r.Revision, Payload: json.RawMessage(`{}`), Reason: "owner no longer wants this check"}
	if op, err := e.app.Execute(t.Context(), owner, env, cmd); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	_, seq := e.message("owner", "status please", "")
	p := e.prepare(seq)
	if strings.Contains(p.Text, "CANCELLED_FOLLOWUP_INSTRUCTION") {
		t.Errorf("cancelled input returned to fresh hosted context without its cancellation state:\n%s", p.Text)
	}
}

func TestFollowupDeltaRespectsProjectScope(t *testing.T) {
	e := newEnv(t)
	app.WithFollowups(true)(e.app)
	var w protocol.WorkItem
	raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{IssuePlan: protocol.IssuePlan{Title: "Check", Intent: "Existing goal"}}, app.CreateIssue{})
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		before, err = store.LatestEventSeq(t.Context(), tx.Q())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cmd := app.FollowupCommand{Action: "create"}
	e.exec(owner, cmd.Operation(), protocol.Target{Type: "work_item", ID: w.WorkItemID}, &w.Revision, protocol.FollowupSpec{Purpose: "Check", Instruction: "Read facts", Kind: "at", At: testkit.Epoch.Add(time.Hour).UnixMilli(), Timezone: "UTC"}, cmd)
	for _, project := range []string{"other", ""} {
		var s Snapshot
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			through, err := store.LatestEventSeq(t.Context(), tx.Q())
			if err != nil {
				return err
			}
			return e.b.gatherDelta(t.Context(), tx.Q(), &s, before, through, protocol.Principal{}, &scope{project: project, labels: true, projects: map[string]string{}})
		}); err != nil {
			t.Fatal(err)
		}
		if project == "other" && len(s.Changes) > 0 {
			t.Fatalf("foreign followup leaked: %v", s.Changes)
		}
		if project == "" && len(s.Changes) != 1 {
			t.Fatalf("own followup missing: %v", s.Changes)
		}
	}
}

func TestAttentionWithoutIssueReminderReachesMainContext(t *testing.T) {
	e := newEnv(t)
	clock := testkit.NewClock()
	e.app = app.New(e.store, e.ids, clock.Now, app.WithFollowups(true))
	e.b = New(e.app, e.store, e.ids, Config{})
	at := clock.Now().Add(30 * time.Hour).UnixMilli()
	var attention protocol.Attention
	raw := e.exec(owner, "attentions.create", protocol.Target{Type: "attention"}, nil, protocol.CreateAttentionPayload{Kind: "action", Title: "Send the signed form", LatestActionAt: &at}, app.AttentionCommand{Action: "create"})
	if err := json.Unmarshal(raw, &attention); err != nil {
		t.Fatal(err)
	}
	if attention.IssueID != "" || attention.LatestActionDueRuleID == "" {
		t.Fatal(attention)
	}
	if err := e.b.Confirm(t.Context(), e.prepare(0).ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(31 * time.Hour)
	e.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "followups.sweep", protocol.Target{Type: "followups"}, nil, struct{}{}, app.SweepFollowups{})
	packet, err := e.b.Prepare(t.Context(), thread, 0)
	if err != nil {
		t.Fatal("actual no-Issue reminder broke main context", err)
	}
	if packet == nil || packet.Full || !strings.Contains(packet.Text, attention.AttentionID) || !strings.Contains(packet.Text, attention.Title) {
		t.Fatalf("missing actual Attention reminder: %+v", packet)
	}
}
