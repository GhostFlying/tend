//go:build linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

func (h workerCloseHost) cgroup(ctx context.Context, pid int, hierarchy string) (string, error) {
	body, err := readWorkerCloseKernel(ctx, filepath.Join(h.procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		matches := hierarchy == "" && fields[0] == "0" && fields[1] == ""
		if hierarchy == protocol.WorkerCloseSystemdV1 {
			for _, controller := range strings.Split(fields[1], ",") {
				matches = matches || controller == "name=systemd"
			}
		}
		if matches {
			path := fields[2]
			if filepath.IsAbs(path) && filepath.Clean(path) == path {
				return path, nil
			}
		}
	}
	return "", errors.New("process cgroup hierarchy identity unavailable")
}

func (h workerCloseHost) systemdCgroupMount(ctx context.Context) (string, error) {
	body, err := readWorkerCloseKernel(ctx, filepath.Join(h.procRoot, "self/mountinfo"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		left, right := strings.Fields(before), strings.Fields(after)
		if !ok || len(left) < 6 || len(right) < 3 || right[0] != "cgroup" {
			continue
		}
		systemd := false
		for _, option := range strings.Split(right[2], ",") {
			systemd = systemd || option == "name=systemd"
		}
		if !systemd {
			continue
		}
		unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
		root, mount := unescape.Replace(left[3]), unescape.Replace(left[4])
		if root != "/" || !filepath.IsAbs(mount) || filepath.Clean(mount) != mount {
			return "", errors.New("systemd cgroup mount must expose its actual filesystem root")
		}
		return mount, nil
	}
	return "", errors.New("named systemd cgroup filesystem mount is unavailable")
}

func (h workerCloseHost) executionBoundary(ctx context.Context, pid int, unit string) (protocol.WorkerCloseBoundary, error) {
	path, err := h.cgroup(ctx, pid, "")
	boundary := protocol.WorkerCloseBoundary{Unit: unit, CgroupPath: path}
	if err == nil && path != "/" && filepath.Base(path) == unit {
		return boundary, nil
	}
	path, err = h.cgroup(ctx, pid, protocol.WorkerCloseSystemdV1)
	if err != nil {
		return boundary, err
	}
	if path == "/" || filepath.Base(path) != unit {
		return boundary, errors.New("runner must occupy the exact named unit cgroup boundary")
	}
	mount, err := h.systemdCgroupMount(ctx)
	if err != nil {
		return boundary, err
	}
	return protocol.WorkerCloseBoundary{Unit: unit, CgroupPath: path, Hierarchy: protocol.WorkerCloseSystemdV1, FilesystemRoot: mount}, nil
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
	boundary, err := h.executionBoundary(ctx, q.RunnerPID, q.Unit)
	if err != nil {
		return in, err
	}
	filesystemRoot := h.cgroupRoot
	if boundary.FilesystemRoot != "" {
		filesystemRoot = boundary.FilesystemRoot
	}
	path := boundary.CgroupPath
	boundaryPath := filepath.Join(filesystemRoot, path)
	device, inode, err := workerCloseDirectoryIdentity(boundaryPath)
	if err != nil {
		return in, err
	}
	if boundary.Hierarchy == "" {
		if _, err := readWorkerCloseKernel(ctx, filepath.Join(boundaryPath, "cgroup.events")); err != nil {
			return in, err
		}
	}
	rootDevice, rootInode, err := workerCloseDirectoryIdentity(filesystemRoot)
	if err != nil {
		return in, err
	}
	in = protocol.WorkerCloseIntent{
		Version: 1, RequestID: q.RequestID, RunID: q.RunID, ThreadID: q.ThreadID,
		StateDirectory: q.StateDirectory, Directory: owned.Dir, OwnedRecordSHA256: q.OwnedSHA256,
		Runner: runner, Processes: append([]protocol.WorkerCloseProcess{owned.Process}, owned.Detached...),
		Boundary:    boundary,
		OperatorUID: h.uid, UnitAcknowledged: q.AcknowledgeUnit, PreparedAt: h.now().UTC().Format(time.RFC3339Nano),
	}
	in.Boundary.Device, in.Boundary.Inode = device, inode
	in.Boundary.RootDevice, in.Boundary.RootInode = rootDevice, rootInode
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
		processCgroup, err := h.cgroup(ctx, original.PID, boundary.Hierarchy)
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
	againBoundary, err := h.executionBoundary(ctx, runner.PID, q.Unit)
	if err != nil {
		return in, err
	}
	againDevice, againInode, err := workerCloseDirectoryIdentity(boundaryPath)
	if err != nil {
		return in, err
	}
	if againBoundary != boundary || againDevice != device || againInode != inode {
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
	filesystemRoot := h.cgroupRoot
	if boundary.FilesystemRoot != "" {
		filesystemRoot = boundary.FilesystemRoot
	}
	if boundary.Hierarchy == protocol.WorkerCloseSystemdV1 {
		mount, err := h.systemdCgroupMount(ctx)
		if err != nil {
			return "", err
		}
		if mount != filesystemRoot {
			return "", errors.New("original systemd cgroup mount changed")
		}
	}
	rootDevice, rootInode, err := workerCloseDirectoryIdentity(filesystemRoot)
	if err != nil {
		return "", err
	}
	if rootDevice != boundary.RootDevice || rootInode != boundary.RootInode {
		return "", errors.New("original cgroup filesystem root was replaced or unmounted")
	}
	path := filepath.Join(filesystemRoot, boundary.CgroupPath)
	device, inode, err := workerCloseDirectoryIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		if boundary.Hierarchy == protocol.WorkerCloseSystemdV1 {
			if err := confirmWorkerCloseV1InodeRemoved(ctx, filesystemRoot, boundary); err != nil {
				return "", err
			}
		}
		return "kernel_removed", nil
	}
	if err != nil {
		return "", err
	}
	if device != boundary.Device || inode != boundary.Inode {
		return "", errors.New("original cgroup directory was replaced")
	}
	if boundary.Hierarchy == protocol.WorkerCloseSystemdV1 {
		return "", errors.New("original systemd v1 cgroup still exists; wait for kernel directory removal before confirmation")
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

// V1 permits same-parent renames, including an ancestor of the bound unit.
// Missing the old pathname therefore needs one complete, bounded metadata
// lookup before it can mean that the original cgroup inode was removed.
func confirmWorkerCloseV1InodeRemoved(ctx context.Context, root string, boundary protocol.WorkerCloseBoundary) error {
	const directoryLimit = 4096
	directories := 0
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("read original systemd cgroup removal metadata: %w", walkErr)
		}
		if !entry.IsDir() {
			return nil
		}
		directories++
		if directories > directoryLimit {
			return fmt.Errorf("original systemd cgroup removal lookup exceeded %d directories; inode absence remains unconfirmed", directoryLimit)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect original systemd cgroup removal metadata: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("systemd cgroup directory identity unavailable during removal lookup")
		}
		if uint64(stat.Dev) != boundary.RootDevice {
			return errors.New("systemd cgroup removal lookup crossed a filesystem device; inode absence remains unconfirmed")
		}
		if uint64(stat.Dev) == boundary.Device && stat.Ino == boundary.Inode {
			return errors.New("original systemd v1 cgroup inode still exists at a renamed path; kernel removal remains unconfirmed")
		}
		return nil
	})
}
