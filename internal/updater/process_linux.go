//go:build linux

package updater

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type LinuxProcess struct{}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := sha256.New()
	if _, err := io.Copy(s, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(s.Sum(nil)), nil
}
func (LinuxProcess) Inspect(ctx context.Context, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if pid <= 1 {
		return Identity{}, ErrNotRunning
	}
	root := filepath.Join("/proc", strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(root, "stat"))
	if os.IsNotExist(err) {
		return Identity{}, ErrNotRunning
	}
	if err != nil {
		return Identity{}, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return Identity{}, ErrUnknown
	}
	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 20 {
		return Identity{}, ErrUnknown
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return Identity{}, ErrNotRunning
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Identity{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return Identity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return Identity{}, errors.New("updater: process belongs to another service user")
	}
	sum, err := hashFile(filepath.Join(root, "exe"))
	if err != nil {
		if os.IsNotExist(err) {
			return Identity{}, ErrNotRunning
		}
		return Identity{}, err
	}
	env, err := os.ReadFile(filepath.Join(root, "environ"))
	if os.IsNotExist(err) {
		return Identity{}, ErrNotRunning
	}
	if err != nil {
		// A process exiting between stat and environ can temporarily return
		// EACCES. Only explicit exit evidence makes this a stopped process.
		if processExited(root) {
			return Identity{}, ErrNotRunning
		}
		return Identity{}, err
	}
	launch := ""
	for _, v := range strings.Split(string(env), "\x00") {
		if strings.HasPrefix(v, "TEND_UPDATER_LAUNCH_ID=") {
			launch = strings.TrimPrefix(v, "TEND_UPDATER_LAUNCH_ID=")
		}
	}
	// Re-read stat after executable/env reads to detect an exit or PID reuse.
	again, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return Identity{}, ErrNotRunning
	}
	if !sameStart(again, ticks) {
		return Identity{}, ErrUnknown
	}
	return Identity{PID: pid, StartTicks: ticks, LaunchID: launch, SHA256: sum}, nil
}
func sameStart(raw []byte, ticks uint64) bool {
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return false
	}
	f := strings.Fields(string(raw)[i+1:])
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return false
	}
	n, err := strconv.ParseUint(f[19], 10, 64)
	return err == nil && n == ticks
}
func (p LinuxProcess) Find(ctx context.Context, launchID, sha string) ([]Identity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var found []Identity
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		// Filter by same UID before reading sensitive process environment.
		info, err := e.Info()
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != os.Getuid() {
			continue
		}
		env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil {
			continue
		}
		matched := false
		for _, v := range strings.Split(string(env), "\x00") {
			if v == "TEND_UPDATER_LAUNCH_ID="+launchID {
				matched = true
			}
		}
		if !matched {
			continue
		}
		id, err := p.Inspect(ctx, pid)
		if errors.Is(err, ErrNotRunning) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if id.SHA256 != sha {
			continue
		}
		found = append(found, id)
	}
	return found, nil
}
func readEnvFiles(paths []string) ([]string, error) {
	values := map[string]string{}
	for _, v := range os.Environ() {
		k, x, ok := strings.Cut(v, "=")
		if ok {
			values[k] = x
		}
	}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			key, value, ok := strings.Cut(line, "=")
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if !ok || !validEnvName(key) {
				_ = f.Close()
				return nil, errors.New("updater: invalid environment file assignment")
			}
			if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2 {
				value = value[1 : len(value)-1]
			} else if strings.HasPrefix(value, "\"") {
				v, err := strconv.Unquote(value)
				if err != nil {
					_ = f.Close()
					return nil, errors.New("updater: invalid environment file quoting")
				}
				value = v
			}
			// Shell expansion/execution is intentionally unsupported. Env files are data.
			if strings.ContainsRune(value, 0) {
				_ = f.Close()
				return nil, errors.New("updater: invalid environment value")
			}
			values[key] = value
		}
		err = scan.Err()
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(values))
	for k, v := range values {
		result = append(result, k+"="+v)
	}
	return result, nil
}
func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
func (p LinuxProcess) Start(ctx context.Context, binary string, cfg Config, extra map[string]string) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	env, err := readEnvFiles(cfg.EnvFiles)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	log, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	defer log.Close()
	cmd := exec.Command(binary, cfg.Args...)
	cmd.Env = env
	cmd.Dir = cfg.Dir
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrNotStarted, err)
	}
	// Reap children started by this supervisor. Recovered children are reaped by
	// their adopting init process; their identity is still checked through /proc.
	go func() { _ = cmd.Wait() }()
	id, err := p.Inspect(context.WithoutCancel(ctx), cmd.Process.Pid)
	if err != nil {
		return Identity{}, fmt.Errorf("updater: child started but identity unavailable: %w", ErrUnknown)
	}
	return id, nil
}
func (p LinuxProcess) Stop(ctx context.Context, id Identity, timeout time.Duration) error {
	current, err := p.Inspect(ctx, id.PID)
	if errors.Is(err, ErrNotRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.StartTicks != id.StartTicks || current.SHA256 != id.SHA256 {
		return errors.New("updater: refusing to signal reused process identity")
	}
	// pidfd pins the inspected process: recheck after opening so PID reuse cannot
	// turn SIGTERM into a signal to a different process.
	fd, _, errno := syscall.Syscall(434, uintptr(id.PID), 0, 0) // pidfd_open, Linux >= 5.3.
	if errno != 0 {
		return fmt.Errorf("updater: pin process identity: %w", errno)
	}
	defer syscall.Close(int(fd))
	current, err = p.Inspect(ctx, id.PID)
	if errors.Is(err, ErrNotRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.StartTicks != id.StartTicks || current.SHA256 != id.SHA256 {
		return ErrUnknown
	}
	_, _, errno = syscall.Syscall6(424, fd, uintptr(syscall.SIGTERM), 0, 0, 0, 0) // pidfd_send_signal.
	if errno != 0 && errno != syscall.ESRCH {
		return fmt.Errorf("updater: stop process: %w", errno)
	}
	return observeSignalledProcess(ctx, id, timeout, p.Inspect)
}

// Only Stop may enter this observation loop, after pinning and signalling once.
func observeSignalledProcess(ctx context.Context, id Identity, timeout time.Duration, inspect func(context.Context, int) (Identity, error)) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := inspect(ctx, id.PID)
		if errors.Is(err, ErrNotRunning) {
			return nil
		}
		// Observations may transiently fail while the pinned child exits. An
		// unreadable process is not proof of exit; retain the stop deadline.
		if err == nil && current.StartTicks != id.StartTicks {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("updater: process did not stop; no forced termination attempted")
		case <-ticker.C:
		}
	}
}

func (LinuxProcess) VerifyCommand(ctx context.Context, pid int, cfg Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return err
	}
	env, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return err
	}
	launch, runtimeID := "", ""
	for _, entry := range strings.Split(string(env), "\x00") {
		if strings.HasPrefix(entry, "TEND_UPDATER_LAUNCH_ID=") {
			launch = strings.TrimPrefix(entry, "TEND_UPDATER_LAUNCH_ID=")
		}
		if strings.HasPrefix(entry, "TEND_UPDATER_PROCESS_INSTANCE_ID=") {
			runtimeID = strings.TrimPrefix(entry, "TEND_UPDATER_PROCESS_INSTANCE_ID=")
		}
	}
	if launch == "" || runtimeID != launch {
		return errors.New("updater: bootstrap launch ID must match runtime instance")
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if len(args) != len(cfg.Args)+1 {
		return errors.New("updater: bootstrap arguments differ from configured service")
	}
	for i, v := range cfg.Args {
		if args[i+1] != v {
			return errors.New("updater: bootstrap arguments differ from configured service")
		}
	}
	return nil
}

func processExited(root string) bool {
	raw, err := os.ReadFile(filepath.Join(root, "stat"))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return false
	}
	fields := strings.Fields(string(raw)[end+1:])
	return len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X")
}
