package runner

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRetainedStrictQuestionYieldRejectsUnprovenSettlement(t *testing.T) {
	type scenario struct {
		name         string
		phase        string
		stopped      bool
		held         bool
		noAuthority  bool
		newAuthority bool
		assignment   func(*protocol.Assignment)
		request      func(*journal.Request)
	}
	for _, tc := range []scenario{
		{name: "physical stop unknown"},
		{name: "new attachment authority", stopped: true, newAuthority: true},
		{name: "no original authority", stopped: true, noAuthority: true},
		{name: "owner holds original worker", stopped: true, held: true},
		{name: "wrong final phase", stopped: true, phase: journal.PhaseSubmitting},
		{name: "unnegotiated handoff", stopped: true, assignment: func(as *protocol.Assignment) { as.QuestionHandoffs = false }},
		{name: "manager is not an ordinary run", stopped: true, assignment: func(as *protocol.Assignment) { as.Manager = &protocol.IssueManagerAssignment{} }},
		{name: "ordinary release", stopped: true, request: func(req *journal.Request) {
			req.Envelope.Operation = protocol.OpRunsRelease
			req.Envelope.Payload = json.RawMessage(`{"reason":"unresolved original outcome"}`)
		}},
		{name: "other run target", stopped: true, request: func(req *journal.Request) { req.Envelope.Target.ID = "other-run" }},
		{name: "other target type", stopped: true, request: func(req *journal.Request) { req.Envelope.Target.Type = "task" }},
		{name: "different original thread", stopped: true, request: func(req *journal.Request) {
			req.Envelope.Payload = json.RawMessage(`{"decision_id":"original-question","thread_id":"other-thread"}`)
		}},
		{name: "missing decision", stopped: true, request: func(req *journal.Request) {
			req.Envelope.Payload = json.RawMessage(`{"thread_id":"original-thread"}`)
		}},
		{name: "extra payload input", stopped: true, request: func(req *journal.Request) {
			req.Envelope.Payload = json.RawMessage(`{"decision_id":"original-question","thread_id":"original-thread","new_prompt":"forbidden"}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original-run", LeaseID: "original-lease", TaskID: "original-task", Role: protocol.RoleImplement, Generation: 7, SpecVersion: 3, Desired: protocol.DesiredRun, QuestionHandoffs: true}
			if tc.assignment != nil {
				tc.assignment(&as)
			}
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
				t.Fatal(err)
			}
			if err := j.SetThread(ctx, as.RunID, "original-thread"); err != nil {
				t.Fatal(err)
			}
			if err := j.SetFenced(ctx, as.RunID, true); err != nil {
				t.Fatal(err)
			}
			if tc.stopped {
				if err := j.ConfirmStopped(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.SetHeld(ctx, as.RunID, tc.held); err != nil {
				t.Fatal(err)
			}
			authority := protocol.RetainedRunResume{RequestID: "original-authority", Mode: "attach"}
			if !tc.noAuthority {
				if err := j.SetRetainedResume(ctx, as.RunID, authority); err != nil {
					t.Fatal(err)
				}
			}
			req := journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-yield", Operation: protocol.OpRunsYieldQuestion, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"decision_id":"original-question","thread_id":"original-thread"}`)}}
			if tc.request != nil {
				tc.request(&req)
			}
			phase := tc.phase
			if phase == "" {
				phase = journal.PhaseReleasing
			}
			if err := j.Finish(ctx, as.RunID, phase, req); err != nil {
				t.Fatal(err)
			}
			before, requests := retainedStrictSnapshot(t, j, as.RunID)
			oldAuthority, err := j.RetainedResume(ctx, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			workers := &retainedStrictWorkerProbe{}
			spaces := &retainedStrictWorkspaceProbe{}
			r := New(Config{Journal: j, Workers: retainedStrictWorkers{workers}, Workspaces: spaces, Worker: protocol.WorkerOrca, Now: clock.Now, IDs: testkit.NewIDGen(clock, 7), RetryDelay: time.Hour, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
			if tc.newAuthority {
				authority.RequestID = "replacement-authority"
			}
			retainedStrictHello(t, r, protocol.RunVerdict{RunID: as.RunID, Desired: protocol.DesiredRun, Verdict: protocol.VerdictContinueRetained, RetainedResume: &authority})
			if err := r.freshHello(ctx); err == nil {
				t.Fatal("unproven original handoff was admitted")
			}
			r.wg.Wait()
			after, pending := retainedStrictSnapshot(t, j, as.RunID)
			currentAuthority, err := j.RetainedResume(ctx, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, pending) || !reflect.DeepEqual(oldAuthority, currentAuthority) || workers.starts.Load() != 0 || workers.releases.Load() != 0 || spaces.prepares.Load() != 0 || spaces.reattaches.Load() != 0 || spaces.collections.Load() != 0 || spaces.finishes.Load() != 0 {
				t.Fatal("rejected handoff changed original facts or resources", before, after, requests, pending)
			}
		})
	}
}
