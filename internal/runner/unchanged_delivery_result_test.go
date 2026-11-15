package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type unchangedCompletionWorkspace struct {
	completionWorkspaces
	collectError error
	snapshots    int
}

func (s *unchangedCompletionWorkspace) Collect(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error) {
	return protocol.ArtifactPayload{}, s.collectError
}

func (s *unchangedCompletionWorkspace) Snapshot(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error) {
	s.snapshots++
	return protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{{Repo: "tend", Path: "tend", BaseSHA: strings.Repeat("a", 40), CommitSHA: strings.Repeat("b", 40), Changed: false}}}, nil
}

func TestUnchangedDeliveryReportPreservesActualMountState(t *testing.T) {
	for _, scenario := range []string{"stage", "upgrade", "ordinary", "duplicate-upgrade", "collection-error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			clock := testkit.NewClock()
			j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "run_unchanged", TaskID: "task_unchanged", Role: protocol.RoleImplement, Desired: protocol.DesiredRun, Spec: json.RawMessage(`{"deliverables":["commit","report"]}`)}
			if err := j.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			if err := j.SetThread(ctx, as.RunID, "owned-thread"); err != nil {
				t.Fatal(err)
			}
			report := &protocol.ResultReport{Body: "Retain the actual repository state"}
			switch scenario {
			case "stage":
				report.Predeployment = &protocol.PredeploymentResult{DeliveryID: "pr", HeadSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40)}
			case "upgrade", "collection-error":
				report.Evidence = []string{"upgrade:actual-install"}
			case "duplicate-upgrade":
				report.Evidence = []string{"upgrade:one", "upgrade:two"}
			}
			body, _ := json.Marshal(harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: report}})
			if err := j.WorkerDone(ctx, as.RunID, body); err != nil {
				t.Fatal(err)
			}
			spaces := &unchangedCompletionWorkspace{collectError: ErrNoChanges}
			if scenario == "collection-error" {
				spaces.collectError = ErrWorkspaceMoved
			}
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Workers: &completionWorkers{}, Workspaces: spaces, RetryDelay: time.Nanosecond})
			err = r.completeWork(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun}, body)
			request, final, readErr := j.RequestForRun(ctx, as.RunID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "stage" || scenario == "upgrade" {
				if err != nil || spaces.snapshots != 1 || !final || request.Envelope.Operation != protocol.OpRunsSubmitArtifact {
					t.Fatal("state report was lost", err, spaces.snapshots)
				}
				var p protocol.ArtifactPayload
				if err := json.Unmarshal(request.Envelope.Payload, &p); err != nil {
					t.Fatal(err)
				}
				if len(p.Mounts) != 1 || p.Mounts[0].Changed || p.Mounts[0].SnapshotSHA != "" {
					t.Fatal("fabricated repository changes", p)
				}
			} else {
				want := ErrNoChanges
				if scenario == "collection-error" {
					want = ErrWorkspaceMoved
				}
				if !errors.Is(err, want) || spaces.snapshots != 0 || final {
					t.Fatal("ordinary output or collection error bypassed", err, spaces.snapshots)
				}
			}
		})
	}
}
