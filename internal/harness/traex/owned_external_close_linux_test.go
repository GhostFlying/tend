//go:build linux

package traex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func externalOwnedCloseFixture(t *testing.T) (harness.WorkerSpec, ownedWorkerProcess, protocol.WorkerCloseProof) {
	t.Helper()
	dir := t.TempDir()
	spec := harness.WorkerSpec{RunID: "original-run", Thread: "original-thread", Dir: filepath.Join(dir, "workspace"), StateDir: filepath.Join(dir, "state")}
	record := ownedWorkerProcess{Version: 1, RunID: spec.RunID, ThreadID: spec.Thread, Dir: spec.Dir, Process: ownedProcessIdentity{PID: 200, StartTicks: "2000", BootID: "original-boot"}, Detached: []ownedProcessIdentity{{PID: 300, StartTicks: "3000", BootID: "original-boot"}}}
	if err := saveOwnedWorkerProcess(spec, record); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(spec.StateDir, ownedProcessFile))
	if err != nil {
		t.Fatal(err)
	}
	in := protocol.WorkerCloseIntent{Version: 1, RequestID: "original-request", RunID: spec.RunID, ThreadID: spec.Thread, Directory: spec.Dir, StateDirectory: spec.StateDir, OwnedRecordSHA256: protocol.WorkerCloseSHA256(body), Runner: protocol.WorkerCloseProcess{PID: 100, StartTicks: "1000", BootID: "original-boot"}, Processes: []protocol.WorkerCloseProcess{{PID: 200, StartTicks: "2000", BootID: "original-boot"}, {PID: 300, StartTicks: "3000", BootID: "original-boot"}}, Boundary: protocol.WorkerCloseBoundary{Unit: "original.service", CgroupPath: "/system.slice/original.service", Device: 1, Inode: 2, RootDevice: 1, RootInode: 1}, OperatorUID: 1000, UnitAcknowledged: true, PreparedAt: "2026-01-01T00:00:00Z"}
	return spec, record, externalOwnedCloseProof(t, in)
}

func externalOwnedCloseProof(t *testing.T, in protocol.WorkerCloseIntent) protocol.WorkerCloseProof {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.WorkerCloseProof{Version: 1, Source: protocol.ExternalExecutionBoundary, IntentSHA256: protocol.WorkerCloseSHA256(body), Intent: in, Observations: []protocol.WorkerCloseObservation{{ObservedAt: "2026-01-01T00:01:00Z", State: "empty"}, {ObservedAt: "2026-01-01T00:01:01Z", State: "kernel_removed"}}}
}

func saveExternalOwnedCloseProof(t *testing.T, spec harness.WorkerSpec, digest string, proof protocol.WorkerCloseProof) {
	t.Helper()
	body, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spec.StateDir, protocol.WorkerCloseProofFilename(digest)), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExternalOwnedCloseProofBindsOriginalExecution(t *testing.T) {
	spec, record, proof := externalOwnedCloseFixture(t)
	if ok, err := verifyExternalOwnedClose(context.Background(), spec, record); ok || err != nil {
		t.Fatalf("missing proof=%v err=%v", ok, err)
	}
	// A historical proof for another attempt must not block this original Run.
	saveExternalOwnedCloseProof(t, spec, strings.Repeat("a", 64), proof)
	if ok, err := verifyExternalOwnedClose(context.Background(), spec, record); ok || err != nil {
		t.Fatalf("unrelated proof=%v err=%v", ok, err)
	}
	saveExternalOwnedCloseProof(t, spec, proof.Intent.OwnedRecordSHA256, proof)
	if ok, err := verifyExternalOwnedClose(context.Background(), spec, record); !ok || err != nil {
		t.Fatalf("exact original historical proof=%v err=%v", ok, err)
	}
	// The official hook records the external observation scope, never original
	// native Close success, and remains idempotent after the owned SHA changes.
	workers := NewWorkers(WorkerOptions{})
	if err := workers.StopOwnedWorker(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	got, err := workers.ownedWorkerProcess(spec)
	if err != nil || got.CloseObserved || got.ReconciledCloseScope != protocol.ExternalExecutionBoundary {
		t.Fatalf("wrong close provenance: %+v err=%v", got, err)
	}
	if err := workers.StopOwnedWorker(context.Background(), spec); err != nil {
		t.Fatalf("external historical scope reentry: %v", err)
	}
}

func TestExternalOwnedCloseProofRejectsMismatchedFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*protocol.WorkerCloseProof)
	}{
		{"wrong_run", func(p *protocol.WorkerCloseProof) { p.Intent.RunID = "another-run" }},
		{"wrong_thread", func(p *protocol.WorkerCloseProof) { p.Intent.ThreadID = "another-thread" }},
		{"wrong_directory", func(p *protocol.WorkerCloseProof) { p.Intent.Directory += "-other" }},
		{"wrong_state_directory", func(p *protocol.WorkerCloseProof) { p.Intent.StateDirectory += "-other" }},
		{"wrong_owned_sha", func(p *protocol.WorkerCloseProof) { p.Intent.OwnedRecordSHA256 = strings.Repeat("b", 64) }},
		{"different_process_set", func(p *protocol.WorkerCloseProof) { p.Intent.Processes[1].PID = 999 }},
		{"different_runner_boot", func(p *protocol.WorkerCloseProof) { p.Intent.Runner.BootID = "another-boot" }},
		{"unacknowledged_unit", func(p *protocol.WorkerCloseProof) { p.Intent.UnitAcknowledged = false }},
		{"not_external_source", func(p *protocol.WorkerCloseProof) { p.Source = "native_close" }},
		{"active_observation", func(p *protocol.WorkerCloseProof) { p.Observations[0].State = "populated" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, record, proof := externalOwnedCloseFixture(t)
			digest := proof.Intent.OwnedRecordSHA256
			tc.edit(&proof)
			body, err := json.Marshal(proof.Intent)
			if err != nil {
				t.Fatal(err)
			}
			proof.IntentSHA256 = protocol.WorkerCloseSHA256(body)
			saveExternalOwnedCloseProof(t, spec, digest, proof)
			if ok, err := verifyExternalOwnedClose(context.Background(), spec, record); ok || err == nil {
				t.Fatalf("mismatched proof=%v err=%v", ok, err)
			}
		})
	}
}

func TestExternalOwnedCloseProofPreservesMalformedAndChangedRecords(t *testing.T) {
	spec, record, proof := externalOwnedCloseFixture(t)
	path := filepath.Join(spec.StateDir, protocol.WorkerCloseProofFilename(proof.Intent.OwnedRecordSHA256))
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, err := verifyExternalOwnedClose(context.Background(), spec, record); ok || err == nil {
		t.Fatalf("corrupt matching proof=%v %v", ok, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(spec.StateDir, ownedProcessFile), path); err != nil {
		t.Fatal(err)
	}
	if ok, err := verifyExternalOwnedClose(context.Background(), spec, record); ok || err == nil {
		t.Fatalf("symlink proof=%v %v", ok, err)
	}
	record.ThreadID = "changed"
	if ok, err := verifyExternalOwnedClose(context.Background(), spec, record); ok || err == nil {
		t.Fatalf("changed source=%v %v", ok, err)
	}
}
