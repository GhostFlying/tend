package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type workerCloseOptions struct {
	Action          string `json:"action"`
	StateDirectory  string `json:"state_directory"`
	RequestID       string `json:"request_id"`
	RunID           string `json:"run_id,omitempty"`
	ThreadID        string `json:"thread_id,omitempty"`
	OwnedSHA256     string `json:"owned_record_sha256,omitempty"`
	OwnedMissing    bool   `json:"owned_record_missing,omitempty"`
	Directory       string `json:"directory,omitempty"`
	RunnerPID       int    `json:"runner_pid,omitempty"`
	RunnerStart     string `json:"runner_start_ticks,omitempty"`
	RunnerBoot      string `json:"runner_boot_id,omitempty"`
	Unit            string `json:"unit,omitempty"`
	AcknowledgeUnit bool   `json:"acknowledge_unit_execution,omitempty"`
}

type workerCloseHost struct {
	procRoot, cgroupRoot string
	uid                  int
	now                  func() time.Time
}

type workerCloseOwnedRecord struct {
	Version  int                           `json:"version"`
	RunID    string                        `json:"run_id"`
	ThreadID string                        `json:"thread_id"`
	Dir      string                        `json:"directory"`
	Process  protocol.WorkerCloseProcess   `json:"process"`
	Detached []protocol.WorkerCloseProcess `json:"detached_processes"`
}

func runWorkerClose(ctx context.Context, e *Env, args []string) error {
	return runWorkerCloseOnHost(ctx, e, args, workerCloseHost{procRoot: "/proc", cgroupRoot: "/sys/fs/cgroup", uid: os.Geteuid(), now: time.Now})
}

func runWorkerCloseOnHost(ctx context.Context, e *Env, args []string, host workerCloseHost) error {
	if len(args) == 0 || !slices.Contains(protocol.WorkerCloseActions(), args[0]) {
		return usagef("worker-close requires prepare, confirm or show")
	}
	var q workerCloseOptions
	q.Action = args[0]
	fs := newFlags(e, "worker-close "+q.Action)
	fs.StringVar(&q.StateDirectory, "state-dir", "", "absolute original Run state directory; no configuration or credentials are read")
	fs.StringVar(&q.RequestID, "request-id", "", "fixed original local request ID; reuse for show and interrupted confirmation")
	if q.Action == "prepare" {
		fs.StringVar(&q.RunID, "run", "", "exact original Run ID")
		fs.StringVar(&q.ThreadID, "thread", "", "exact original native thread ID")
		fs.StringVar(&q.OwnedSHA256, "owned-sha256", "", "SHA256 of original owned-appserver-process.json bytes")
		fs.BoolVar(&q.OwnedMissing, "owned-missing", false, "original owned record is absent; bind the entire acknowledged execution unit instead")
		fs.StringVar(&q.Directory, "directory", "", "exact original absolute workspace directory, required with --owned-missing")
		fs.IntVar(&q.RunnerPID, "runner-pid", 0, "original live runner PID")
		fs.StringVar(&q.RunnerStart, "runner-start-ticks", "", "original runner /proc start ticks")
		fs.StringVar(&q.RunnerBoot, "runner-boot-id", "", "original runner kernel boot ID")
		fs.StringVar(&q.Unit, "unit", "", "original systemd unit containing the execution boundary (unified v2 or named systemd v1)")
		fs.BoolVar(&q.AcknowledgeUnit, "acknowledge-unit-execution", false, "operator confirms this unit contains the original execution, including descendants")
	}
	dry := fs.Bool("dry-run", false, "print exact local input without reading or writing execution state")
	timeout := fs.Duration("timeout", 10*time.Second, "bounded local operation timeout, at most 20s")
	fs.Bool("json", true, "print the complete local intent/proof receipt as JSON")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || q.RequestID == "" || !filepath.IsAbs(q.StateDirectory) || *timeout <= 0 || *timeout > 20*time.Second {
		return usagef("worker-close requires --state-dir absolute-path, --request-id and --timeout in (0,20s]")
	}
	if q.Action == "prepare" {
		digest, decodeErr := hex.DecodeString(q.OwnedSHA256)
		ownedValid := !q.OwnedMissing && decodeErr == nil && len(digest) == 32 && q.Directory == ""
		missingValid := q.OwnedMissing && q.OwnedSHA256 == "" && filepath.IsAbs(q.Directory)
		if (!ownedValid && !missingValid) || q.RunID == "" || q.ThreadID == "" || q.RunnerPID <= 1 || q.RunnerStart == "" || q.RunnerBoot == "" || q.Unit == "" || !q.AcknowledgeUnit {
			return usagef("worker-close prepare requires exact Run/thread/runner PID/start/boot, --unit, --acknowledge-unit-execution and either --owned-sha256 or --owned-missing with --directory")
		}
	}
	if *dry {
		return (owner{e: e, json: true}).printJSON(struct {
			Capability string             `json:"capability"`
			Input      workerCloseOptions `json:"input"`
		}{"local EXEC worker-close " + q.Action, q})
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	receipt, err := executeWorkerClose(ctx, q, host)
	if err != nil {
		return fmt.Errorf("worker-close %s: %w; inspect the same request with worker-close show --state-dir %q --request-id %q; do not replace the request ID", q.Action, err, q.StateDirectory, q.RequestID)
	}
	return (owner{e: e, json: true}).printJSON(receipt)
}

func executeWorkerClose(ctx context.Context, q workerCloseOptions, host workerCloseHost) (protocol.WorkerCloseReceipt, error) {
	var receipt protocol.WorkerCloseReceipt
	path := filepath.Join(q.StateDirectory, protocol.WorkerCloseIntentFilename(q.RequestID))
	body, err := readWorkerCloseFile(ctx, path)
	if err == nil {
		if err := decodeWorkerCloseJSON(body, &receipt.Intent); err != nil {
			return receipt, err
		}
		if err := receipt.Intent.Validate(); err != nil {
			return receipt, err
		}
		in := receipt.Intent
		if in.RequestID != q.RequestID || in.StateDirectory != q.StateDirectory || q.Action == "prepare" && (in.RunID != q.RunID || in.ThreadID != q.ThreadID || in.OwnedRecordSHA256 != q.OwnedSHA256 || in.OwnedRecordMissing != q.OwnedMissing || q.OwnedMissing && in.Directory != q.Directory || in.Runner.PID != q.RunnerPID || in.Runner.StartTicks != q.RunnerStart || in.Runner.BootID != q.RunnerBoot || in.Boundary.Unit != q.Unit) {
			return receipt, errors.New("local request input differs from its original intent")
		}
	} else if errors.Is(err, os.ErrNotExist) && q.Action == "prepare" {
		receipt.Intent, err = prepareWorkerClose(ctx, q, host)
		if err != nil {
			return receipt, err
		}
		body, err = json.Marshal(receipt.Intent)
		if err == nil && receipt.Intent.OwnedRecordMissing {
			err = os.MkdirAll(q.StateDirectory, 0700)
		}
		if err == nil {
			err = publishWorkerCloseFile(ctx, path, body)
		}
		if err != nil {
			return receipt, err
		}
	} else {
		return receipt, err
	}
	receipt.RequestID, receipt.Phase = q.RequestID, "prepared"
	receipt.IntentPath, receipt.IntentSHA256 = path, protocol.WorkerCloseSHA256(body)
	proofPath := filepath.Join(q.StateDirectory, receipt.Intent.ProofFilename())
	proofBody, err := readWorkerCloseFile(ctx, proofPath)
	var proof protocol.WorkerCloseProof
	if err == nil {
		if err := decodeWorkerCloseJSON(proofBody, &proof); err != nil {
			return receipt, err
		}
		if err := proof.Validate(); err != nil {
			return receipt, err
		}
		if proof.IntentSHA256 != receipt.IntentSHA256 {
			return receipt, errors.New("external proof belongs to a different intent")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if q.Action != "confirm" {
			return receipt, nil
		}
		proof, err = confirmWorkerClose(ctx, receipt.Intent, receipt.IntentSHA256, host)
		if err != nil {
			return receipt, err
		}
		proofBody, err = json.Marshal(proof)
		if err == nil {
			err = publishWorkerCloseFile(ctx, proofPath, proofBody)
		}
		if err != nil {
			return receipt, err
		}
	} else {
		return receipt, err
	}
	receipt.Phase, receipt.ProofPath, receipt.ProofSHA256, receipt.Proof = "confirmed", proofPath, protocol.WorkerCloseSHA256(proofBody), &proof
	return receipt, nil
}

func readWorkerCloseFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("local close record must be a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("local close record changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(body) > 1<<20 {
		return nil, errors.New("local close record exceeds size bound")
	}
	return body, err
}

func decodeWorkerCloseJSON(body []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing data in local close record")
	}
	return nil
}

// Link publishes immutable facts exclusively. An interrupted caller inspects
// the original request; identical facts are safe to read again, never overwritten.
func publishWorkerCloseFile(ctx context.Context, path string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".owned-external-close-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		prior, readErr := readWorkerCloseFile(ctx, path)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(prior, body) {
			return errors.New("local request already published a different immutable fact; inspect its original receipt")
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
