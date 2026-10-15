package journal

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func open(t *testing.T, path string) *Journal {
	t.Helper()
	j, err := Open(context.Background(), path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestJournalSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	claim := Request{Envelope: protocol.Envelope{RequestID: "req_1", Operation: protocol.OpRunnerClaim, Target: protocol.Target{Type: "host", ID: "h"}, Payload: json.RawMessage(`{}`)}}
	if err := j.PutRequest(ctx, claim); err != nil {
		t.Fatal(err)
	}
	as := protocol.Assignment{RunID: "run_1", LeaseID: "lse_1", RunToken: "tok", Role: protocol.RoleImplement}
	if err := j.AcceptAssignment(ctx, as, "req_1"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"item.started", "item.completed"} {
		if err := j.AppendEvent(ctx, "run_1", protocol.RunEvent{Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()

	j = open(t, path)
	defer j.Close()
	if reqs, err := j.Requests(ctx); err != nil || len(reqs) != 0 {
		t.Errorf("requests after accepting the claim = %+v, %v", reqs, err)
	}
	runs, err := j.Runs(ctx)
	if err != nil || len(runs) != 1 || runs[0].Assignment.RunToken != "tok" || runs[0].Phase != PhaseClaimed {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	events, err := j.PendingEvents(ctx, "run_1", 10)
	if err != nil || len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if err := j.AckEvents(ctx, "run_1", 1); err != nil {
		t.Fatal(err)
	}
	if err := j.AppendEvent(ctx, "run_1", protocol.RunEvent{Kind: "item.started"}); err != nil {
		t.Fatal(err)
	}
	if events, _ := j.PendingEvents(ctx, "run_1", 10); len(events) != 2 || events[1].Seq != 3 {
		t.Errorf("sequence numbers must never be reused: %+v", events)
	}

	final := Request{RunID: "run_1", Envelope: protocol.Envelope{RequestID: "req_2", Operation: protocol.OpRunsSubmitArtifact, Target: protocol.Target{Type: "run", ID: "run_1"}}}
	if err := j.Finish(ctx, "run_1", PhaseSubmitting, final); err != nil {
		t.Fatal(err)
	}
	if r, ok, err := j.RequestForRun(ctx, "run_1"); err != nil || !ok || r.Envelope.RequestID != "req_2" {
		t.Errorf("final request = %+v, %v, %v", r, ok, err)
	}
	if err := j.Done(ctx, "run_1"); err != nil {
		t.Fatal(err)
	}
	if runs, _ := j.Runs(ctx); len(runs) != 0 {
		t.Errorf("done run still listed: %+v", runs)
	}
	if _, ok, _ := j.RequestForRun(ctx, "run_1"); ok {
		t.Error("done run kept its request")
	}
}
