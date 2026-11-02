package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
)

// nativeBinding records transport identity, not a second work or control ledger.
// An unanswered thread/start is retained as pending and requires reconciliation.
type nativeBinding struct {
	RunID        string              `json:"run_id,omitempty"`
	Usage        *nativeusage.Source `json:"usage,omitempty"`
	UsageStopped bool                `json:"usage_stopped,omitempty"`
	Cwd          string              `json:"cwd"`
	Thread       string              `json:"thread,omitempty"`
	Pending      bool                `json:"pending,omitempty"`
}

func (b *Broker) bindingPath(runID string) string {
	hash := sha256.Sum256([]byte(runID))
	return filepath.Join(b.cfg.Cwd, "run-bindings", hex.EncodeToString(hash[:])+".json")
}

func (b *Broker) bindHello(runID string, hello Hello) error {
	b.usageMu.Lock()
	defer b.usageMu.Unlock()
	if b.cfg.Cwd == "" {
		return errors.New("native binding directory is unavailable")
	}
	raw, err := os.ReadFile(b.bindingPath(runID))
	if errors.Is(err, os.ErrNotExist) {
		if hello.ThreadID != "" {
			return errors.New("native session has no original run binding")
		}
		return b.saveBinding(runID, nativeBinding{Cwd: hello.Cwd})
	}
	var pin nativeBinding
	if err != nil || json.Unmarshal(raw, &pin) != nil || pin.Pending || pin.Cwd != hello.Cwd || pin.Thread != hello.ThreadID {
		return errors.New("native session binding requires reconciliation")
	}
	if pin.UsageStopped {
		pin.UsageStopped = false
		return b.saveBinding(runID, pin)
	}
	return nil
}

func (b *Broker) saveBinding(runID string, pin nativeBinding) error {
	pin.RunID = runID
	path := b.bindingPath(runID)
	if err := writePrivate(path, pin); err != nil {
		return err
	}
	// Persist the identity before exposing a thread receipt or starting a native
	// operation. A missing binding on recovery always fails closed.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil || runtime.GOOS == "windows" {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	return err
}

// saveThreadBinding updates native thread identity without replacing the
// independently recorded source or process-stop evidence.
func (b *Broker) saveThreadBinding(runID string, pin nativeBinding) error {
	b.usageMu.Lock()
	defer b.usageMu.Unlock()
	raw, err := os.ReadFile(b.bindingPath(runID))
	if err == nil {
		var prior nativeBinding
		if err := json.Unmarshal(raw, &prior); err != nil {
			return err
		}
		if prior.Cwd != pin.Cwd || (prior.Thread != "" && pin.Thread != "" && prior.Thread != pin.Thread) {
			return errors.New("native thread binding changed")
		}
		pin.Usage, pin.UsageStopped = prior.Usage, prior.UsageStopped
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return b.saveBinding(runID, pin)
}
