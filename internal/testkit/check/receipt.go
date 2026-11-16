package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// A receipt records the native process result separately from cancellation.
// Missing exit codes mean no process result was obtained, never success.
type receipt struct {
	Phase            string    `json:"phase"`
	PID              int       `json:"pid,omitempty"`
	Started          time.Time `json:"started_at"`
	Finished         time.Time `json:"finished_at,omitempty"`
	ExitCode         *int      `json:"exit_code"`
	NativeResult     string    `json:"native_process_result,omitempty"`
	Outcome          string    `json:"outcome"`
	Error            string    `json:"error,omitempty"`
	Cause            string    `json:"cause,omitempty"`
	CleanupCompleted bool      `json:"cleanup_completed"`
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// writeReceipt publishes only complete JSON; an interrupted replacement leaves
// the previous started record visible instead of a partial terminal record.
func writeReceipt(directory string, value receipt) (err error) {
	if directory == "" {
		return nil
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".receipt-")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(directory, value.Phase+".json")); err != nil {
		return err
	}
	// Remove on success is already satisfied by Rename.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func receiptDirectory(root string) (string, error) {
	if root == "" {
		return "", nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create check report root: %w", err)
	}
	return os.MkdirTemp(root, "check-")
}
