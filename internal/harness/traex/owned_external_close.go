package traex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// verifyExternalOwnedClose consumes a historical operator observation. It does
// not inspect a successor's current cgroup: the same unit may now legitimately
// contain a new runner. The immutable proof binds the exact original bytes.
func verifyExternalOwnedClose(ctx context.Context, spec harness.WorkerSpec, record ownedWorkerProcess) (bool, error) {
	body, err := readExternalOwnedCloseFile(ctx, filepath.Join(spec.StateDir, ownedProcessFile))
	if err != nil {
		return false, err
	}
	var current ownedWorkerProcess
	if err := json.Unmarshal(body, &current); err != nil {
		return false, err
	}
	if !reflect.DeepEqual(current, record) {
		return false, errors.New("traex: original owned record changed before external close verification")
	}
	digest := protocol.WorkerCloseSHA256(body)
	proofBody, err := readExternalOwnedCloseFile(ctx, filepath.Join(spec.StateDir, protocol.WorkerCloseProofFilename(digest)))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("traex: read external close proof: %w", err)
	}
	var proof protocol.WorkerCloseProof
	if err := json.Unmarshal(proofBody, &proof); err != nil {
		return false, fmt.Errorf("traex: decode external close proof: %w", err)
	}
	if err := proof.Validate(); err != nil {
		return false, err
	}
	in := proof.Intent
	if in.OwnedRecordSHA256 != digest || in.RunID != spec.RunID || in.RunID != record.RunID || in.ThreadID != record.ThreadID || spec.Thread != "" && in.ThreadID != spec.Thread || in.Directory != record.Dir || in.Directory != spec.Dir || in.StateDirectory != spec.StateDir {
		return false, errors.New("traex: external close proof does not bind the original Run, thread and ownership")
	}
	var original struct {
		Process  protocol.WorkerCloseProcess   `json:"process"`
		Detached []protocol.WorkerCloseProcess `json:"detached_processes"`
	}
	if err := json.Unmarshal(body, &original); err != nil {
		return false, err
	}
	processes := append([]protocol.WorkerCloseProcess{original.Process}, original.Detached...)
	if !reflect.DeepEqual(in.Processes, processes) || in.Runner.BootID != original.Process.BootID {
		return false, errors.New("traex: external close proof process identities differ from original ownership")
	}
	again, err := readExternalOwnedCloseFile(ctx, filepath.Join(spec.StateDir, ownedProcessFile))
	if err != nil {
		return false, err
	}
	if protocol.WorkerCloseSHA256(again) != digest {
		return false, errors.New("traex: owned record changed during external close verification")
	}
	return true, nil
}

func readExternalOwnedCloseFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("traex: external close record must be a bounded regular file")
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
		return nil, errors.New("traex: external close record changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if len(body) > 1<<20 {
		return nil, errors.New("traex: external close record exceeds size bound")
	}
	return body, err
}
