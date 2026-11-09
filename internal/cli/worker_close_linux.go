//go:build linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (h workerCloseHost) boot(ctx context.Context) (string, error) {
	body, err := readWorkerCloseKernel(ctx, filepath.Join(h.procRoot, "sys/kernel/random/boot_id"))
	return strings.TrimSpace(string(body)), err
}

func readWorkerCloseKernel(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// proc/sysfs report zero lengths; read bounds rather than stat lengths.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 65537))
	if len(body) > 65536 {
		return nil, errors.New("execution boundary metadata exceeds size bound")
	}
	return body, err
}

func (h workerCloseHost) process(ctx context.Context, pid int, boot string) (protocol.WorkerCloseProcess, bool, error) {
	var identity protocol.WorkerCloseProcess
	body, err := readWorkerCloseKernel(ctx, filepath.Join(h.procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return identity, false, err
	}
	i := strings.LastIndex(string(body), ") ")
	if i < 0 {
		return identity, false, errors.New("malformed process stat")
	}
	fields := strings.Fields(string(body)[i+2:])
	if len(fields) < 20 {
		return identity, false, errors.New("incomplete process stat")
	}
	identity = protocol.WorkerCloseProcess{PID: pid, StartTicks: fields[19], BootID: boot}
	return identity, fields[0] != "Z" && fields[0] != "X", nil
}

func (h workerCloseHost) cgroup(ctx context.Context, pid int) (string, error) {
	body, err := readWorkerCloseKernel(ctx, filepath.Join(h.procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "0::") {
			path := strings.TrimPrefix(line, "0::")
			if filepath.IsAbs(path) && filepath.Clean(path) == path {
				return path, nil
			}
		}
	}
	return "", errors.New("process cgroup v2 identity unavailable")
}

func workerCloseDirectoryIdentity(path string) (uint64, uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() {
		return 0, 0, errors.New("original cgroup is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("kernel directory identity unavailable")
	}
	return uint64(stat.Dev), stat.Ino, nil
}

func prepareWorkerClose(ctx context.Context, q workerCloseOptions, h workerCloseHost) (protocol.WorkerCloseIntent, error) {
	var in protocol.WorkerCloseIntent
	body, err := readWorkerCloseFile(ctx, filepath.Join(q.StateDirectory, "owned-appserver-process.json"))
	if err != nil {
		return in, err
	}
	if protocol.WorkerCloseSHA256(body) != q.OwnedSHA256 {
		return in, errors.New("original owned record SHA256 changed")
	}
	var owned workerCloseOwnedRecord
	if err := decodeWorkerCloseJSON(body, &owned); err != nil {
		return in, err
	}
	if owned.Version != 1 || owned.RunID != q.RunID || owned.ThreadID != q.ThreadID || !filepath.IsAbs(owned.Dir) {
		return in, errors.New("original owned Run/thread/directory differs")
	}
	boot, err := h.boot(ctx)
	if err != nil {
		return in, err
	}
	runner, active, err := h.process(ctx, q.RunnerPID, boot)
	if err != nil {
		return in, err
	}
	if !active || runner.StartTicks != q.RunnerStart || runner.BootID != q.RunnerBoot {
		return in, errors.New("original runner is not the exact live process")
	}
	path, err := h.cgroup(ctx, q.RunnerPID)
	if err != nil {
		return in, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || filepath.Base(path) != q.Unit {
		return in, errors.New("runner must occupy the exact named cgroup v2 unit boundary")
	}
	boundaryPath := filepath.Join(h.cgroupRoot, path)
	device, inode, err := workerCloseDirectoryIdentity(boundaryPath)
	if err != nil {
		return in, err
	}
	if _, err := readWorkerCloseKernel(ctx, filepath.Join(boundaryPath, "cgroup.events")); err != nil {
		return in, err
	}
	rootDevice, rootInode, err := workerCloseDirectoryIdentity(h.cgroupRoot)
	if err != nil {
		return in, err
	}
	in = protocol.WorkerCloseIntent{
		Version: 1, RequestID: q.RequestID, RunID: q.RunID, ThreadID: q.ThreadID,
		StateDirectory: q.StateDirectory, Directory: owned.Dir, OwnedRecordSHA256: q.OwnedSHA256,
		Runner: runner, Processes: append([]protocol.WorkerCloseProcess{owned.Process}, owned.Detached...),
		Boundary:    protocol.WorkerCloseBoundary{Unit: q.Unit, CgroupPath: path, Device: device, Inode: inode, RootDevice: rootDevice, RootInode: rootInode},
		OperatorUID: h.uid, UnitAcknowledged: q.AcknowledgeUnit, PreparedAt: h.now().UTC().Format(time.RFC3339Nano),
	}
	if err := in.Validate(); err != nil {
		return in, err
	}
	for _, original := range in.Processes {
		current, _, err := h.process(ctx, original.PID, boot)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return in, err
		}
		if current != original {
			return in, fmt.Errorf("original execution PID %d was reused during prepare", original.PID)
		}
		processCgroup, err := h.cgroup(ctx, original.PID)
		if err != nil {
			return in, err
		}
		if processCgroup != path && !strings.HasPrefix(processCgroup, path+"/") {
			return in, fmt.Errorf("original PID %d is outside the acknowledged unit boundary", original.PID)
		}
	}
	// Revalidate the mutable source and live runner after collecting its boundary.
	current, err := readWorkerCloseFile(ctx, filepath.Join(q.StateDirectory, "owned-appserver-process.json"))
	if err != nil {
		return in, err
	}
	again, active, err := h.process(ctx, runner.PID, boot)
	if err != nil {
		return in, err
	}
	if !active || again != runner || protocol.WorkerCloseSHA256(current) != q.OwnedSHA256 {
		return in, errors.New("original ownership or runner changed during prepare")
	}
	againCgroup, err := h.cgroup(ctx, runner.PID)
	if err != nil {
		return in, err
	}
	againDevice, againInode, err := workerCloseDirectoryIdentity(boundaryPath)
	if err != nil {
		return in, err
	}
	if againCgroup != path || againDevice != device || againInode != inode {
		return in, errors.New("original runner cgroup changed during prepare")
	}
	return in, nil
}

func confirmWorkerClose(ctx context.Context, in protocol.WorkerCloseIntent, intentSHA string, h workerCloseHost) (protocol.WorkerCloseProof, error) {
	proof := protocol.WorkerCloseProof{Version: 1, Source: protocol.ExternalExecutionBoundary, OperatorUID: h.uid, IntentSHA256: intentSHA, Intent: in}
	for i := 0; i < 2; i++ {
		body, err := readWorkerCloseFile(ctx, filepath.Join(in.StateDirectory, "owned-appserver-process.json"))
		if err != nil {
			return proof, err
		}
		if protocol.WorkerCloseSHA256(body) != in.OwnedRecordSHA256 {
			return proof, errors.New("original owned record changed before confirmation")
		}
		boot, err := h.boot(ctx)
		if err != nil {
			return proof, err
		}
		if boot != in.Runner.BootID {
			return proof, errors.New("kernel boot changed; the prepared boundary is no longer current")
		}
		for _, original := range append([]protocol.WorkerCloseProcess{in.Runner}, in.Processes...) {
			current, active, err := h.process(ctx, original.PID, boot)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return proof, err
			}
			if current != original {
				return proof, fmt.Errorf("original PID %d was reused; no close proof written", original.PID)
			}
			if active {
				return proof, fmt.Errorf("original PID %d remains active", original.PID)
			}
		}
		state, err := observeWorkerCloseBoundary(ctx, in.Boundary, h)
		if err != nil {
			return proof, err
		}
		proof.Observations = append(proof.Observations, protocol.WorkerCloseObservation{ObservedAt: h.now().UTC().Format(time.RFC3339Nano), State: state})
	}
	return proof, proof.Validate()
}

func observeWorkerCloseBoundary(ctx context.Context, boundary protocol.WorkerCloseBoundary, h workerCloseHost) (string, error) {
	rootDevice, rootInode, err := workerCloseDirectoryIdentity(h.cgroupRoot)
	if err != nil {
		return "", err
	}
	if rootDevice != boundary.RootDevice || rootInode != boundary.RootInode {
		return "", errors.New("original cgroup filesystem root was replaced or unmounted")
	}
	path := filepath.Join(h.cgroupRoot, boundary.CgroupPath)
	device, inode, err := workerCloseDirectoryIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return "kernel_removed", nil
	}
	if err != nil {
		return "", err
	}
	if device != boundary.Device || inode != boundary.Inode {
		return "", errors.New("original cgroup directory was replaced")
	}
	events, err := readWorkerCloseKernel(ctx, filepath.Join(path, "cgroup.events"))
	if err != nil {
		return "", err
	}
	found := false
	for _, line := range strings.Split(string(events), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "populated" {
			if fields[1] != "0" {
				return "", errors.New("original cgroup still contains execution, including descendant cgroups")
			}
			found = true
		}
	}
	if !found {
		return "", errors.New("recursive cgroup populated observation unavailable")
	}
	procs, err := readWorkerCloseKernel(ctx, filepath.Join(path, "cgroup.procs"))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(procs)) != "" {
		return "", errors.New("original cgroup.procs is not empty")
	}
	againDevice, againInode, err := workerCloseDirectoryIdentity(path)
	if err != nil {
		return "", err
	}
	if device != againDevice || inode != againInode {
		return "", errors.New("original cgroup changed during observation")
	}
	return "empty", nil
}
