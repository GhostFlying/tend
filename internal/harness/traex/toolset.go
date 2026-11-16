package traex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/harness"
)

// WorkerToolsetHash identifies the full schema and descriptions actually sent
// at thread creation. Native resume does not install a replacement tool menu.
func WorkerToolsetHash(spec harness.WorkerSpec) string {
	raw, _ := json.Marshal(workerTools(spec))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type workerToolset struct {
	RunID    string `json:"run_id"`
	ThreadID string `json:"thread_id"`
	Hash     string `json:"toolset_hash"`
}

const workerToolsetFile = "native-toolset.json"

func prepareWorkerToolset(spec harness.WorkerSpec) (harness.WorkerSpec, error) {
	if spec.Thread == "" {
		return spec, nil
	}
	saved := spec.ThreadToolsetHash
	if saved == "" {
		state, run := spec.StateDir, spec.RunID
		if previous, thread := spec.StoppedPredecessor(); previous != "" && thread == spec.Thread && spec.PredecessorStateDir != "" {
			state, run = spec.PredecessorStateDir, previous
		}
		if state != "" {
			raw, err := os.ReadFile(filepath.Join(state, workerToolsetFile))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return spec, err
			}
			if err == nil {
				var pin workerToolset
				if json.Unmarshal(raw, &pin) != nil || pin.RunID != run || pin.ThreadID != spec.Thread {
					return spec, fmt.Errorf("%w: original native toolset binding differs", harness.ErrRetainedResumePending)
				}
				saved = pin.Hash
			}
		}
	}
	if saved != "" && saved == WorkerToolsetHash(spec) {
		return spec, nil
	}
	previous, thread := spec.StoppedPredecessor()
	if spec.RetainedResume == nil && previous != "" && previous != spec.RunID && thread == spec.Thread {
		// Only the server's stopped predecessor edge permits dropping optional
		// native history. The frozen/current durable Brief remains authoritative.
		spec.Thread = ""
		spec.ThreadToolsetHash = ""
		return spec, nil
	}
	return spec, fmt.Errorf("%w: native tool schema changed or is unknown; reconcile original effects and use the normal stopped Task session continuation", harness.ErrRetainedResumePending)
}

func saveWorkerToolset(spec harness.WorkerSpec, thread string) error {
	if spec.StateDir == "" {
		return nil
	}
	raw, err := json.Marshal(workerToolset{RunID: spec.RunID, ThreadID: thread, Hash: WorkerToolsetHash(spec)})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(spec.StateDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(spec.StateDir, ".native-toolset-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(spec.StateDir, workerToolsetFile)); err != nil {
		return err
	}
	dir, err := os.Open(spec.StateDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
