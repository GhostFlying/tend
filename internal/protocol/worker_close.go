package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// These local maintenance facts are not server/runner messages or native Close
// receipts. The operator binds the original execution boundary before stopping it.
const ExternalExecutionBoundary = "external_execution_boundary"

func WorkerCloseActions() []string { return []string{"prepare", "confirm", "show"} }

type WorkerCloseProcess struct {
	PID        int    `json:"pid"`
	StartTicks string `json:"start_ticks"`
	BootID     string `json:"boot_id"`
}

type WorkerCloseBoundary struct {
	Unit       string `json:"unit"`
	CgroupPath string `json:"cgroup_path"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
	RootDevice uint64 `json:"root_device"`
	RootInode  uint64 `json:"root_inode"`
}

type WorkerCloseIntent struct {
	Version           int                  `json:"version"`
	RequestID         string               `json:"request_id"`
	RunID             string               `json:"run_id"`
	ThreadID          string               `json:"thread_id"`
	StateDirectory    string               `json:"state_directory"`
	Directory         string               `json:"directory"`
	OwnedRecordSHA256 string               `json:"owned_record_sha256"`
	Runner            WorkerCloseProcess   `json:"runner"`
	Processes         []WorkerCloseProcess `json:"processes"`
	Boundary          WorkerCloseBoundary  `json:"boundary"`
	OperatorUID       int                  `json:"operator_uid"`
	UnitAcknowledged  bool                 `json:"unit_execution_acknowledged"`
	PreparedAt        string               `json:"prepared_at"`
}

type WorkerCloseObservation struct {
	ObservedAt string `json:"observed_at"`
	// empty means both recursive cgroup.events populated=0 and empty
	// cgroup.procs were read from the original device/inode. kernel_removed
	// means the previously bound cgroup directory no longer exists.
	State string `json:"state"`
}

type WorkerCloseProof struct {
	Version      int                      `json:"version"`
	Source       string                   `json:"source"`
	OperatorUID  int                      `json:"operator_uid"`
	IntentSHA256 string                   `json:"intent_sha256"`
	Intent       WorkerCloseIntent        `json:"intent"`
	Observations []WorkerCloseObservation `json:"observations"`
}

type WorkerCloseReceipt struct {
	RequestID    string            `json:"request_id"`
	Phase        string            `json:"phase"`
	IntentPath   string            `json:"intent_path"`
	IntentSHA256 string            `json:"intent_sha256"`
	Intent       WorkerCloseIntent `json:"intent"`
	ProofPath    string            `json:"proof_path,omitempty"`
	ProofSHA256  string            `json:"proof_sha256,omitempty"`
	Proof        *WorkerCloseProof `json:"proof,omitempty"`
}

func WorkerCloseSHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func WorkerCloseIntentFilename(requestID string) string {
	return "owned-external-close-intent-" + WorkerCloseSHA256([]byte(requestID)) + ".json"
}

func WorkerCloseProofFilename(ownedSHA256 string) string {
	return "owned-external-close-" + ownedSHA256 + ".json"
}

func (p WorkerCloseProcess) Validate() error {
	if p.PID <= 1 || p.StartTicks == "" || p.BootID == "" {
		return errors.New("worker-close: incomplete process identity")
	}
	return nil
}

func (in WorkerCloseIntent) Validate() error {
	digest, err := hex.DecodeString(in.OwnedRecordSHA256)
	if err != nil || len(digest) != sha256.Size || in.Version != 1 || in.RequestID == "" || in.RunID == "" || in.ThreadID == "" || !filepath.IsAbs(in.StateDirectory) || !filepath.IsAbs(in.Directory) || in.OperatorUID < 0 || in.PreparedAt == "" {
		return errors.New("worker-close: incomplete intent binding")
	}
	if err := in.Runner.Validate(); err != nil {
		return err
	}
	if !in.UnitAcknowledged || in.Boundary.Unit == "" || !filepath.IsAbs(in.Boundary.CgroupPath) || in.Boundary.CgroupPath == "/" || in.Boundary.Device == 0 || in.Boundary.Inode == 0 || in.Boundary.RootDevice == 0 || in.Boundary.RootInode == 0 || len(in.Processes) == 0 {
		return errors.New("worker-close: incomplete execution boundary")
	}
	for _, p := range in.Processes {
		if err := p.Validate(); err != nil {
			return err
		}
		if p.BootID != in.Runner.BootID {
			return errors.New("worker-close: process boot differs from runner")
		}
	}
	return nil
}

func (p WorkerCloseProof) Validate() error {
	if p.Version != 1 || p.Source != ExternalExecutionBoundary || p.OperatorUID < 0 || len(p.Observations) != 2 {
		return errors.New("worker-close: unsupported external close proof")
	}
	if err := p.Intent.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(p.Intent)
	if err != nil || WorkerCloseSHA256(body) != p.IntentSHA256 {
		return errors.New("worker-close: intent digest differs from proof")
	}
	for _, observation := range p.Observations {
		if observation.ObservedAt == "" || observation.State != "empty" && observation.State != "kernel_removed" {
			return fmt.Errorf("worker-close: invalid boundary observation %q", observation.State)
		}
	}
	return nil
}
