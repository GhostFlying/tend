package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// A lost response still has its original request identity. Only an observed,
// successfully decoded result retires the local attempt; not the real resource.
func TestFault_DeferredWorkspaceReleaseResponse(t *testing.T) {
	for _, reply := range []string{"deferred", "lost then deferred", "malformed", "business failure", "deferred held", "deferred unknown"} {
		t.Run(reply, func(t *testing.T) {
			f := newWorkspaceMaintenanceFixture(t)
			ctx := t.Context()
			p := protocol.WorkspaceRelease{WorkspaceID: "workspace", TaskID: "task"}
			if err := f.j.QueueWorkspaceRelease(ctx, p); err != nil {
				t.Fatal(err)
			}
			env, err := f.r.newEnvelope(protocol.OpWorkspaceRelease, protocol.Target{Type: "host", ID: "host"}, p)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.j.PutRequest(ctx, journal.Request{Envelope: *env}); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var ids []string
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var got protocol.Envelope
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				ids = append(ids, got.RequestID)
				attempt := len(ids)
				mu.Unlock()
				if reply == "lost then deferred" && attempt == 1 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				op := protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{"released":false}`)}
				if reply == "malformed" {
					op.Result = json.RawMessage(`[]`)
				}
				if reply == "business failure" {
					op.Status = protocol.OpFailed
					op.Error = protocol.Errorf(protocol.CodeInvalidTransition, "resource unavailable")
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &op})
			}))
			defer remote.Close()
			cfg := f.r.cfg
			cfg.ServerURL = remote.URL
			cfg.HTTPClient = remote.Client()
			f.r = New(cfg)
			err = f.r.releaseWorkspace(ctx, env)
			if reply == "lost then deferred" {
				if err == nil {
					t.Fatal("lost response was treated as confirmed")
				}
				requests, _ := f.j.Requests(ctx)
				releases, _ := f.j.WorkspaceReleases(ctx)
				if len(requests) != 1 || requests[0].Envelope.RequestID != env.RequestID || len(releases) != 1 {
					t.Fatal("unknown response lost durable request or release")
				}
				err = f.r.releaseWorkspace(ctx, &requests[0].Envelope)
				mu.Lock()
				if len(ids) != 2 || ids[0] != ids[1] {
					t.Error("lost response retried with different identity")
				}
				mu.Unlock()
			}
			requests, _ := f.j.Requests(ctx)
			releases, _ := f.j.WorkspaceReleases(ctx)
			if reply == "malformed" || reply == "business failure" {
				if err == nil || len(requests) != 1 || len(releases) != 1 {
					t.Fatalf("unconfirmed release retired: requests=%d releases=%d err=%v", len(requests), len(releases), err)
				}
				return
			}
			if err != nil || len(requests) != 0 || len(releases) != 0 {
				t.Fatalf("confirmed deferred attempt still blocks maintenance: requests=%d releases=%d err=%v", len(requests), len(releases), err)
			}
			if reply == "deferred held" || reply == "deferred unknown" {
				as := protocol.Assignment{RunID: "active", TaskID: p.TaskID, Desired: protocol.DesiredRun, Workspace: protocol.AssignmentWorkspace{ID: p.WorkspaceID}}
				if err := f.j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerOrca); err != nil {
					t.Fatal(err)
				}
				if _, err := f.j.NextAttempt(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
				if err := f.j.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
					t.Fatal(err)
				}
				if err := f.j.SetThread(ctx, as.RunID, `{"launch":"unconfirmed-native-launch"}`); err != nil {
					t.Fatal(err)
				}
				if reply == "deferred held" {
					if err := f.j.SetHeld(ctx, as.RunID, true); err != nil {
						t.Fatal(err)
					}
				}
				f.r.bootstrapMaintenance = true
				f.r.resourceReconciled = true
				runtime, err := f.r.runtimeObservation(ctx)
				if err != nil || runtime.Mode == protocol.RunnerReady || len(runtime.Blockers) == 0 {
					t.Fatalf("unconfirmed run became ready: %+v %v", runtime, err)
				}
				run, err := f.j.HistoricalRun(ctx, as.RunID)
				if err != nil || run.StoppedConfirmed {
					t.Fatalf("deferred response forged run stop proof: %+v %v", run, err)
				}
			}
		})
	}
}
