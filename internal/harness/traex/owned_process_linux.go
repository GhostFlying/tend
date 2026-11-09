//go:build linux

package traex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ownedProcessIdentity struct {
	PID        int    `json:"pid"`
	StartTicks string `json:"start_ticks"`
	BootID     string `json:"boot_id"`
}

func ownedProcessSupported() bool { return true }

func readOwnedProcessStat(pid int) (identity ownedProcessIdentity, group int, active bool, err error) {
	identity, group, _, active, err = readOwnedProcessDetail(pid)
	return identity, group, active, err
}

func readOwnedProcessDetail(pid int) (identity ownedProcessIdentity, group, parent int, active bool, err error) {
	body, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return identity, 0, 0, false, err
	}
	// comm may contain closing parentheses, so use the last delimiter.
	index := strings.LastIndex(string(body), ") ")
	if index < 0 {
		return identity, 0, 0, false, errors.New("traex: malformed native process identity")
	}
	fields := strings.Fields(string(body)[index+2:])
	if len(fields) < 20 {
		return identity, 0, 0, false, errors.New("traex: incomplete native process identity")
	}
	group, err = strconv.Atoi(fields[2])
	if err != nil {
		return identity, 0, 0, false, err
	}
	parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return identity, 0, 0, false, err
	}
	identity = ownedProcessIdentity{PID: pid, StartTicks: fields[19]}
	return identity, group, parent, fields[0] != "Z" && fields[0] != "X", nil
}

func readOwnedProcess(pid int) (identity ownedProcessIdentity, group int, active bool, err error) {
	identity, group, active, err = readOwnedProcessStat(pid)
	if err != nil {
		return identity, 0, false, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	identity.BootID = strings.TrimSpace(string(boot))
	return identity, group, active, err
}

func inspectOwnedProcess(pid int) (ownedProcessIdentity, error) {
	identity, group, _, err := readOwnedProcess(pid)
	if err == nil && group != pid {
		err = errors.New("traex: app-server does not own its process group")
	}
	return identity, err
}

func ownedGroupActive(group int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		_, observed, active, err := readOwnedProcessStat(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if observed == group && active {
			return true, nil
		}
	}
	return false, nil
}

func stopOwnedProcess(ctx context.Context, original ownedProcessIdentity, checkDetached func() error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := validOwnedProcessIdentity(original); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(boot)) != original.BootID {
		// A host reboot ended the entire original kernel process set.
		return nil
	}
	current, group, active, err := readOwnedProcess(original.PID)
	if err == nil && (current != original || group != original.PID) {
		return errors.New("traex: original app-server PID was reused; no process was stopped")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil || !active {
		return errOwnedProcessExited
	}
	if err == nil {
		// Pin the leader while checking and signalling its group. os.Process
		// uses a pidfd on supported Linux kernels, so a recycled PID cannot
		// receive the stop signal intended for the original leader.
		process, err := os.FindProcess(original.PID)
		if err != nil {
			return err
		}
		defer process.Release()
		current, group, _, err = readOwnedProcess(original.PID)
		if err == nil && (current != original || group != original.PID) {
			return errors.New("traex: original app-server identity changed before stop")
		}
		if err != nil {
			return err
		}
		if err := process.Signal(syscall.SIGSTOP); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		killed := false
		defer func() {
			if killed {
				return
			}
			current, group, _, err := readOwnedProcess(original.PID)
			if err == nil && current == original && group == original.PID {
				_ = syscall.Kill(-original.PID, syscall.SIGCONT)
			}
		}()
		current, group, _, err = readOwnedProcess(original.PID)
		if err == nil && (current != original || group != original.PID) {
			return errors.New("traex: original app-server identity changed before stop")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			if err := syscall.Kill(-original.PID, syscall.SIGSTOP); err != nil {
				return err
			}
			for {
				frozen, err := ownedGroupFrozen(original.PID)
				if err != nil {
					return err
				}
				if frozen {
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
			if err := checkDetached(); err != nil {
				return err
			}
			if err := syscall.Kill(-original.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			killed = true
		}
	}
	return awaitOwnedProcessStopped(ctx, original)
}

func ownedGroupFrozen(group int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		_, observed, active, err := readOwnedProcessStat(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if observed != group || !active {
			continue
		}
		body, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		index := strings.LastIndex(string(body), ") ")
		if index < 0 || len(body) <= index+2 {
			return false, errors.New("traex: native process stop state is unavailable")
		}
		if state := body[index+2]; state != 'T' && state != 't' && state != 'Z' && state != 'X' {
			return false, nil
		}
	}
	return true, nil
}

func awaitOwnedProcessStopped(ctx context.Context, original ownedProcessIdentity) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if err := confirmOwnedProcessStopped(original); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("traex: original app-server process group closure remains unconfirmed: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// ownedDetachedDescendants observes the original live leader's kernel parent
// tree across process groups and sessions. A PTY/setsid child cannot disappear
// from the recovery decision merely because the original group is empty.
func ownedDetachedDescendants(original ownedProcessIdentity) ([]ownedProcessIdentity, error) {
	if err := validOwnedProcessIdentity(original); err != nil {
		return nil, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(boot)) != original.BootID {
		return nil, nil
	}
	current, group, active, err := readOwnedProcess(original.PID)
	if err != nil || current != original || group != original.PID || !active {
		return nil, errors.New("traex: original live app-server parent tree is unavailable")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	type entry struct {
		identity      ownedProcessIdentity
		parent, group int
		active        bool
	}
	var processes []entry
	for _, item := range entries {
		pid, err := strconv.Atoi(item.Name())
		if err != nil {
			continue
		}
		identity, group, parent, active, err := readOwnedProcessDetail(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		identity.BootID = original.BootID
		processes = append(processes, entry{identity, parent, group, active})
	}
	descendants := map[int]bool{original.PID: true}
	for changed := true; changed; {
		changed = false
		for _, process := range processes {
			if descendants[process.parent] && !descendants[process.identity.PID] {
				descendants[process.identity.PID] = true
				changed = true
			}
		}
	}
	var detached []ownedProcessIdentity
	for _, process := range processes {
		if descendants[process.identity.PID] && process.group != original.PID && process.active {
			detached = append(detached, process.identity)
		}
	}
	return detached, nil
}

func confirmDetachedProcessesStopped(processes []ownedProcessIdentity) error {
	for _, original := range processes {
		current, _, active, err := readOwnedProcess(original.PID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if current == original && active {
			return errors.New("traex: observed detached native execution remains active; original workspace must stay isolated")
		}
	}
	return nil
}

// confirmKnownOwnedProcessSetStopped checks the original group and every
// recorded detached identity, including members left in that identity's group
// after its leader exits. It observes only; it never signals a recycled PID.
func confirmKnownOwnedProcessSetStopped(ctx context.Context, original ownedProcessIdentity, detached []ownedProcessIdentity) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := confirmOwnedProcessStopped(original); err != nil {
		return err
	}
	for _, process := range detached {
		if err := ctx.Err(); err != nil {
			return err
		}
		if process.BootID != original.BootID {
			return errors.New("traex: original detached process boot identity changed")
		}
		if err := validOwnedProcessIdentity(process); err != nil {
			return err
		}
		current, group, active, err := readOwnedProcess(process.PID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && current != process {
			return errors.New("traex: original detached process PID was reused")
		}
		if active {
			return errors.New("traex: original detached native execution remains active")
		}
		groups := []int{process.PID}
		if err == nil && group != process.PID {
			// A recorded descendant need not be its group's leader. A zombie
			// still supplies that group's identity without being live work.
			groups = append(groups, group)
		}
		for _, group := range groups {
			if err := ctx.Err(); err != nil {
				return err
			}
			active, err := ownedGroupActive(group)
			if err != nil {
				return err
			}
			if active {
				return errors.New("traex: original detached process group remains active")
			}
		}
	}
	return ctx.Err()
}

func validOwnedProcessIdentity(original ownedProcessIdentity) error {
	start, err := strconv.ParseUint(original.StartTicks, 10, 64)
	if original.PID <= 1 || err != nil || start == 0 || len(original.BootID) != 36 || original.BootID[8] != '-' || original.BootID[13] != '-' || original.BootID[18] != '-' || original.BootID[23] != '-' {
		return errors.New("traex: incomplete original app-server process identity")
	}
	return nil
}

func confirmOwnedProcessStopped(original ownedProcessIdentity) error {
	if err := validOwnedProcessIdentity(original); err != nil {
		return err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(boot)) != original.BootID {
		// A host reboot ended the entire original kernel process set.
		return nil
	}
	current, group, active, err := readOwnedProcess(original.PID)
	if err == nil && (current != original || group != original.PID) {
		return errors.New("traex: original app-server PID was reused; no process was stopped")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && active {
		return errors.New("traex: original app-server is still active")
	}
	active, err = ownedGroupActive(original.PID)
	if err != nil {
		return err
	}
	if active {
		return errors.New("traex: original app-server descendants are still active")
	}
	return nil
}
