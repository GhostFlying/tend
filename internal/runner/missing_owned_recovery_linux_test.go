//go:build linux

package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type missingOwnedRecoverySpace struct {
	*ownedRecoveryWorkspace
	finished int
}

func (s *missingOwnedRecoverySpace) Finish(context.Context, protocol.Assignment) error {
	s.finished++
	return nil
}

func TestFault_MissingOwnedBoundaryProofReleasesOriginalRunWithoutReplay(t *testing.T) {
	r, as, _, workspace := appserverRecoveryFixture(t)
	spaces := &missingOwnedRecoverySpace{ownedRecoveryWorkspace: workspace}
	r.cfg.Workspaces = spaces
	before, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	r.cfg.Workers = traex.NewWorkers(traex.WorkerOptions{})
	// This journal was reopened with a prior attempted execution and inherited
	// thread. Absence alone must leave both the Run and resources untouched.
	err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun})
	if !isOwnedRecoveryUncertain(err) {
		t.Fatalf("missing ownership treated as closed: %v", err)
	}
	after, afterPending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	if after.StoppedConfirmed || after.Attempts != before.Attempts || len(pending) != len(afterPending) {
		t.Fatal("unknown execution altered")
	}
	// A fixture of the CLI's independently observed closure, not a claim about
	// native Close or the result of the original business work.
	in := protocol.WorkerCloseIntent{Version: 2, RequestID: "same-close-request", RunID: as.RunID, ThreadID: before.Thread, StateDirectory: workspace.prepared.StateDir, Directory: workspace.prepared.Dir,
		OwnedRecordMissing: true, Runner: protocol.WorkerCloseProcess{PID: 100, StartTicks: "1000", BootID: "original-boot"},
		Boundary:    protocol.WorkerCloseBoundary{Unit: "original.service", CgroupPath: "/system.slice/original.service", Device: 1, Inode: 2, RootDevice: 1, RootInode: 1, Hierarchy: protocol.WorkerCloseSystemdV1, FilesystemRoot: "/sys/fs/cgroup/systemd"},
		OperatorUID: os.Geteuid(), UnitAcknowledged: true, PreparedAt: "2026-01-01T00:00:00Z"}
	intent, _ := json.Marshal(in)
	proof := protocol.WorkerCloseProof{Version: 1, Source: protocol.ExternalExecutionBoundary, OperatorUID: os.Geteuid(), IntentSHA256: protocol.WorkerCloseSHA256(intent), Intent: in,
		Observations: []protocol.WorkerCloseObservation{{ObservedAt: "2026-01-01T00:01:00Z", State: "kernel_removed"}, {ObservedAt: "2026-01-01T00:01:01Z", State: "kernel_removed"}}}
	body, _ := json.Marshal(proof)
	if err := os.WriteFile(filepath.Join(in.StateDirectory, protocol.WorkerCloseMissingProofFilename), body, 0600); err != nil {
		t.Fatal(err)
	}
	var sent []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if spaces.finished != 0 {
			t.Error("workspace released before the original server ACK")
		}
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
		}
		sent = append(sent, env)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.execute(t.Context(), t.Context(), as.RunID, &active{as: as, desired: protocol.DesiredRun}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].Operation != protocol.OpRunsRelease || sent[0].Target.ID != as.RunID {
		t.Fatalf("not the original normal release: %+v", sent)
	}
	var release protocol.ReleasePayload
	if err := json.Unmarshal(sent[0].Payload, &release); err != nil {
		t.Fatal(err)
	}
	if release.Reason != "worker_interrupted_after_runner_restart" {
		t.Fatalf("wrong result: %+v", release)
	}
	got, err := r.cfg.Journal.HistoricalRun(t.Context(), as.RunID)
	if err != nil || got.Phase != journal.PhaseDone || !got.StoppedConfirmed || got.Attempts != before.Attempts || got.Thread != before.Thread || got.Assignment.LeaseID != before.Assignment.LeaseID {
		t.Fatalf("identity/stop/settlement lost: %+v %v", got, err)
	}
	if workspace.mutations != 0 || spaces.finished != 1 {
		t.Fatalf("recovery must only clean up after ACK: prepare/collect=%d cleanup=%d", workspace.mutations, spaces.finished)
	}
	if _, err := os.Lstat(filepath.Join(in.StateDirectory, "owned-appserver-process.json")); !os.IsNotExist(err) {
		t.Fatalf("invented native ownership: %v", err)
	}
}
