package journal

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityReconnectReceiptKeepsOriginalAssignmentAndStopProof(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "agent"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "runtime", Revision: 1}, InventoryRevision: 1, SnapshotID: "original", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model", Effort: "high"}, Worker: protocol.WorkerAppServer, BackendID: "git"}
	refs := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	binding := protocol.RunCapabilityBinding{MatchPolicy: "declaration_v1", Pin: pin, Required: refs, RequiredSHA256: protocol.CapabilityRequiredDigest(refs)}
	as := protocol.Assignment{RunID: "run_original", Execution: &pin, CapabilityBinding: &binding, AcceptanceEvidenceFiles: true}
	as.Workspace.BackendID = pin.BackendID
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if _, err := j.NextAttempt(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := j.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := j.db.QueryRowContext(t.Context(), `SELECT assignment_json FROM runs WHERE run_id=?`, as.RunID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	binding.CurrentMatch = &protocol.CapabilityMatch{State: "pending", CheckedAtMillis: 2}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	// A different reconnect must not replace the frozen definition revision.
	changed := binding
	changed.Required = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 2}}
	changed.RequiredSHA256 = protocol.CapabilityRequiredDigest(changed.Required)
	as.CapabilityBinding = &changed
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	run, err := j.Run(t.Context(), as.RunID)
	if err != nil || run.Assignment.CapabilityBinding.Required[0] != refs[0] || run.Assignment.CapabilityBinding.CurrentMatch.State != "pending" || run.Attempts != 1 || !run.StoppedConfirmed || !run.Assignment.AcceptanceEvidenceFiles {
		t.Fatalf("reconnect replaced frozen data or lost stop/evidence facts: %+v %v", run, err)
	}
	var after string
	if err := j.db.QueryRowContext(t.Context(), `SELECT assignment_json FROM runs WHERE run_id=?`, as.RunID).Scan(&after); err != nil || after != original {
		t.Fatal("reconnect rewrote original assignment", err)
	}
}
