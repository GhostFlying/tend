package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type failingPreparation struct {
	completionWorkspaces
	failure protocol.RunPreparationFailure
}

func (f failingPreparation) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	return Prepared{}, &PreparationError{Failure: f.failure}
}

func TestFault_PreparationReleaseNegotiationAndJournalRecovery(t *testing.T) {
	for _, negotiated := range []bool{false, true} {
		t.Run(map[bool]string{false: "old server", true: "new server"}[negotiated], func(t *testing.T) {
			ctx := t.Context()
			clock := testkit.NewClock()
			path := filepath.Join(t.TempDir(), "journal.db")
			j, err := journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close() })
			as := protocol.Assignment{RunID: "run_preparation", TaskID: "task_preparation", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, PreparationDiagnostics: negotiated}
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			f := protocol.RunPreparationFailure{Phase: protocol.PreparationPhaseWorkspace, Code: protocol.PreparationInheritedChanges, WorkspaceID: "workspace", SourceTaskID: "source", RequiredField: protocol.PreparationIncludeField, Changes: []protocol.PreparationChange{}}
			workers := &completionWorkers{}
			var received protocol.ReleasePayload
			var request string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var env protocol.Envelope
				_ = json.NewDecoder(r.Body).Decode(&env)
				_ = json.Unmarshal(env.Payload, &received)
				request = env.RequestID
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer srv.Close()
			cfg := Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Workers: workers, Workspaces: failingPreparation{failure: f}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ServerURL: srv.URL, HTTPClient: srv.Client()}
			r := New(cfg)
			// Save exactly the release payload the execution path would persist,
			// then restart before posting it to the server.
			payload := protocol.ReleasePayload{Reason: "workspace_prepare_failed"}
			if negotiated {
				payload.Preparation = &f
			}
			if err := r.finish(ctx, as, journal.PhaseReleasing, protocol.OpRunsRelease, payload); err != nil {
				t.Fatal(err)
			}
			saved, _, err := j.RequestForRun(ctx, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Journal = j
			r = New(cfg)
			if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			if workers.starts != 0 || request != saved.Envelope.RequestID || received.Reason != "workspace_prepare_failed" {
				t.Fatal("restart launched or replaced release", workers.starts, request, received)
			}
			if negotiated && !reflect.DeepEqual(received.Preparation, &f) || !negotiated && received.Preparation != nil {
				t.Fatal("diagnostic negotiation or persistence changed", received)
			}
			// A fresh attempt also exercises typed preparation -> release, rather
			// than only the explicit persisted-release path above.
			as.RunID = "run_fresh_preparation"
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
				t.Fatal(err)
			}
			if workers.starts != 0 || negotiated && !reflect.DeepEqual(received.Preparation, &f) || !negotiated && received.Preparation != nil {
				t.Fatal("typed preparation lost evidence or called worker", received)
			}
		})
	}
}
