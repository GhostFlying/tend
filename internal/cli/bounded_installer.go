package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type boundedInstallerInvocation struct {
	Installer          string
	InstallerSHA256    string
	Arguments          []string
	Action             string
	Mutating           bool
	RequestID          string
	ExpectedGeneration uint64
	ExpectedPhase      string
	Timeout            time.Duration
	NextCommand        []string
	PinnedInputs       []boundedInstallerInput
}

type boundedInstallerInput struct {
	Flag string
	Data []byte
}

// runPinnedBoundedInstaller bounds the installer wait, not its managed service.
// Losing the installer reply never proves that a stop or launch did not happen.
func runPinnedBoundedInstaller(ctx context.Context, e *Env, in boundedInstallerInvocation) error {
	if in.Timeout <= 0 {
		return usagef("bounded installer requires a positive command timeout")
	}
	commandContext, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	if err := commandContext.Err(); err != nil {
		return err
	}
	digest, err := hex.DecodeString(in.InstallerSHA256)
	if err != nil || len(digest) != sha256.Size {
		return usagef("installer SHA-256 must be a complete hexadecimal digest")
	}
	file, err := os.Open(in.Installer)
	if err != nil {
		return err
	}
	defer file.Close()
	stopClose := context.AfterFunc(commandContext, func() { _ = file.Close() })
	defer stopClose()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return usagef("installer must be a regular executable artifact")
	}
	directory, err := os.MkdirTemp("", "tend-bounded-installer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	executable := filepath.Join(directory, "installer")
	pinned, err := os.OpenFile(executable, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(sum, pinned), file); err != nil {
		_ = pinned.Close()
		return err
	}
	if err := pinned.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != in.InstallerSHA256 {
		return usagef("installer SHA-256 differs from reviewed artifact")
	}
	if err := os.Chmod(executable, 0700); err != nil {
		return err
	}
	arguments := append([]string(nil), in.Arguments...)
	for i, input := range in.PinnedInputs {
		path := filepath.Join(directory, fmt.Sprintf("input-%d.json", i))
		if err := os.WriteFile(path, input.Data, 0600); err != nil {
			return err
		}
		for j := range arguments {
			if arguments[j] == input.Flag && j+1 < len(arguments) {
				arguments[j+1] = path
			}
		}
	}
	command := exec.CommandContext(commandContext, executable, arguments...)
	// A descendant retaining the stdout descriptor must not keep the CLI alive
	// after the installer exits. This does not establish descendant exit.
	command.WaitDelay = 100 * time.Millisecond
	output := &boundedInstallerOutput{limit: 4 << 20}
	command.Stdout = output
	command.Stderr = e.Stderr
	err = command.Run()
	lostReply := err != nil && command.ProcessState != nil && !json.Valid(output.bytes)
	if commandContext.Err() != nil || errors.Is(err, exec.ErrWaitDelay) || lostReply {
		outcome := "observation_unavailable"
		if in.Mutating {
			outcome = "unknown"
		}
		var expectedGeneration *uint64
		if in.Mutating {
			expectedGeneration = &in.ExpectedGeneration
		}
		result := struct {
			Outcome            string   `json:"outcome"`
			Action             string   `json:"action"`
			RequestID          string   `json:"request_id,omitempty"`
			ExpectedGeneration *uint64  `json:"expected_generation,omitempty"`
			ExpectedPhase      string   `json:"expected_phase,omitempty"`
			InstallerOutput    string   `json:"installer_output,omitempty"`
			OutputTruncated    bool     `json:"output_truncated,omitempty"`
			NextCommand        []string `json:"next_command"`
			Recovery           string   `json:"recovery"`
		}{outcome, in.Action, in.RequestID, expectedGeneration, in.ExpectedPhase, string(output.bytes), output.truncated, in.NextCommand,
			"Inspect durable state and reconcile the original request before repeating it. The managed service and native work may still exist; installer termination is not their exit proof."}
		if printErr := json.NewEncoder(e.Stdout).Encode(result); printErr != nil {
			return printErr
		}
		return fmt.Errorf("bounded installer reply unavailable; preserve the original request and authorization: %w", errors.Join(commandContext.Err(), err))
	}
	if _, printErr := e.Stdout.Write(output.bytes); printErr != nil {
		return printErr
	}
	if output.truncated {
		return errors.New("bounded installer output exceeded the limit; inspect durable state before retrying")
	}
	return err
}

type boundedInstallerOutput struct {
	bytes     []byte
	limit     int
	truncated bool
}

func (w *boundedInstallerOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := max(0, w.limit-len(w.bytes))
	if len(p) > remaining {
		w.truncated = true
		p = p[:remaining]
	}
	w.bytes = append(w.bytes, p...)
	return n, nil
}
