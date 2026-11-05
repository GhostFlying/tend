package journal

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityVerificationCrashAndConcurrentRetryDoNotProbeAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	p := protocol.VerifyLocalCapabilityRequest{RequestID: "original", ExpectedRevision: 2, Pin: protocol.RunExecutionPin{Identity: protocol.AgentIdentity{LocalID: "agent"}}, Capability: protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}, MaxAttempts: 1}
	var wg sync.WaitGroup
	starts := make(chan bool, 8)
	for range 8 {
		wg.Go(func() {
			_, fresh, err := j.BeginCapabilityVerification(t.Context(), p, "instance", 1)
			if err != nil {
				t.Error(err)
			}
			starts <- fresh
		})
	}
	wg.Wait()
	close(starts)
	n := 0
	for fresh := range starts {
		if fresh {
			n++
		}
	}
	if n != 1 {
		t.Fatal("more than one probe admitted", n)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	s, fresh, err := j.BeginCapabilityVerification(t.Context(), p, "instance", 1)
	if err != nil || fresh || s.State != "unknown" || s.Sequence < 1 {
		t.Fatal(s, fresh, err)
	}
	p.TimeoutMillis++
	if _, _, err := j.BeginCapabilityVerification(t.Context(), p, "instance", 1); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("same id changed payload", err)
	}
	p.RequestID = "replacement"
	if _, _, err := j.BeginCapabilityVerification(t.Context(), p, "instance", 1); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("unknown request replaced", err)
	}
}

func TestCapabilityRevalidationReceiptDoesNotRewriteAssignment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	pin := protocol.RunExecutionPin{Worker: protocol.WorkerOrca, BackendID: "orca"}
	b := protocol.RunCapabilityBinding{Pin: pin, RequiredSHA256: "original-digest", EvidenceIDs: []string{"original"}}
	as := protocol.Assignment{RunID: "run", Execution: &pin, CapabilityBinding: &b, Workspace: protocol.AssignmentWorkspace{BackendID: "orca"}}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := j.db.QueryRowContext(t.Context(), `SELECT assignment_json FROM runs WHERE run_id='run'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	updated := b
	updated.Revalidation = &protocol.RunCapabilityRevalidation{RequestID: "explicit-recheck", Pin: pin, RequiredSHA256: b.RequiredSHA256, EvidenceIDs: []string{"fresh"}}
	as.CapabilityBinding = &updated
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	run, err := j.Run(t.Context(), "run")
	if err != nil || run.Assignment.CapabilityBinding.Revalidation == nil || run.Assignment.CapabilityBinding.Revalidation.RequestID != "explicit-recheck" {
		t.Fatal(run, err)
	}
	var raw string
	if err := j.db.QueryRowContext(t.Context(), `SELECT assignment_json FROM runs WHERE run_id='run'`).Scan(&raw); err != nil || raw != original {
		t.Fatal("historical bytes changed", err)
	}
	// A replay with a different frozen requirement must not replace the receipt.
	updated.RequiredSHA256 = "substitution"
	as.CapabilityBinding = &updated
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	run, err = j.Run(t.Context(), "run")
	if err != nil || run.Assignment.CapabilityBinding.RequiredSHA256 != b.RequiredSHA256 {
		body, _ := json.Marshal(run)
		t.Fatal(string(body), err)
	}
}
