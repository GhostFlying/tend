package contextbuilder

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/store"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFollowupRegressionCancelledFollowupFullContext(t *testing.T) {
	e := newEnv(t)
	app.WithFollowups(true)(e.app)
	var w protocol.WorkItem
	raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{WorkItemPlan: protocol.WorkItemPlan{Title: "Check", Intent: "Existing goal"}}, app.CreateWorkItem{})
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
	raw := e.exec(owner, "work_items.create", protocol.Target{Type: "work_item"}, nil, protocol.CreateWorkItemPayload{WorkItemPlan: protocol.WorkItemPlan{Title: "Check", Intent: "Existing goal"}}, app.CreateWorkItem{})
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
