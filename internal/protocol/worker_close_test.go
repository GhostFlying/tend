package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// This is the original canonical v2 intent format, before optional hierarchy
// and filesystem_root fields. Its bytes are part of the proof's durable hash.
const legacyWorkerCloseIntentJSON = `{"version":1,"request_id":"original-request","run_id":"original-run","thread_id":"original-thread","state_directory":"/original/state","directory":"/original/workspace","owned_record_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","runner":{"pid":100,"start_ticks":"1000","boot_id":"original-boot"},"processes":[{"pid":200,"start_ticks":"2000","boot_id":"original-boot"}],"boundary":{"unit":"original.service","cgroup_path":"/system.slice/original.service","device":1,"inode":2,"root_device":1,"root_inode":1},"operator_uid":1000,"unit_execution_acknowledged":true,"prepared_at":"2026-01-01T00:00:00Z"}`

func TestWorkerCloseLegacyV2CanonicalProofRemainsReadable(t *testing.T) {
	var in WorkerCloseIntent
	if err := json.Unmarshal([]byte(legacyWorkerCloseIntentJSON), &in); err != nil {
		t.Fatal(err)
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != legacyWorkerCloseIntentJSON {
		t.Fatalf("legacy canonical intent bytes changed: %s", body)
	}
	proof := WorkerCloseProof{Version: 1, Source: ExternalExecutionBoundary, OperatorUID: 1000, IntentSHA256: WorkerCloseSHA256([]byte(legacyWorkerCloseIntentJSON)), Intent: in, Observations: []WorkerCloseObservation{{ObservedAt: "2026-01-01T00:01:00Z", State: "empty"}, {ObservedAt: "2026-01-01T00:01:01Z", State: "kernel_removed"}}}
	if err := proof.Validate(); err != nil {
		t.Fatal("original v2 proof was rejected", err)
	}
}

func TestWorkerCloseSystemdV1ProofRejectsEmptyObservation(t *testing.T) {
	var in WorkerCloseIntent
	if err := json.Unmarshal([]byte(legacyWorkerCloseIntentJSON), &in); err != nil {
		t.Fatal(err)
	}
	in.Boundary.Hierarchy = WorkerCloseSystemdV1
	in.Boundary.FilesystemRoot = "/sys/fs/cgroup/systemd"
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	proof := WorkerCloseProof{Version: 1, Source: ExternalExecutionBoundary, IntentSHA256: WorkerCloseSHA256(body), Intent: in, Observations: []WorkerCloseObservation{{ObservedAt: "2026-01-01T00:01:00Z", State: "empty"}, {ObservedAt: "2026-01-01T00:01:01Z", State: "kernel_removed"}}}
	if err := proof.Validate(); err == nil || !strings.Contains(err.Error(), "kernel directory removal") {
		t.Fatalf("v1 accepted empty member snapshot: %v", err)
	}
	proof.Observations[0].State = "kernel_removed"
	if err := proof.Validate(); err != nil {
		t.Fatal("real v1 removal proof was rejected", err)
	}
	in.Boundary.FilesystemRoot = ""
	if err := in.Validate(); err == nil {
		t.Fatal("v1 intent without actual mountpoint passed")
	}
}
