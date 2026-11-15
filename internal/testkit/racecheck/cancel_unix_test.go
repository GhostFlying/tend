//go:build !windows

package main

import (
	"context"
	"github.com/GhostFlying/tend/internal/testkit/testbudget"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCancellationJoinsCommandAndStopsDescendants(t *testing.T) {
	t.Setenv(testbudget.ProcessGroup, "")
	root := t.TempDir()
	pidFile := filepath.Join(root, "child")
	script := filepath.Join(root, "command")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 120 &\nprintf '%s' $! > \"$CHILD_PID\"\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, append(os.Environ(), "CHILD_PID="+pidFile), filepath.Join(root, "native.jsonl"), script)
	}()
	watchdog := time.NewTimer(10 * time.Second)
	defer watchdog.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	var pid int
	for pid == 0 {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if pid > 0 {
			break
		}
		select {
		case <-watchdog.C:
			t.Fatal("command fixture not ready")
		case <-poll.C:
		}
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(pid, syscall.SIGKILL)
		<-done
		t.Fatal("command cancellation left a descendant holding the report pipe")
	}
}

func TestCommandGroupsInheritOnlyAnExplicitOwningCheck(t *testing.T) {
	for _, owner := range []string{"", "owned"} {
		t.Run(owner, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := exec.CommandContext(ctx, "sleep", "120")
			env := testbudget.Environment(os.Environ(), map[string]string{testbudget.ProcessGroup: owner})
			cleanup := containCommand(cmd, env)
			defer cleanup()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			group, err := syscall.Getpgid(cmd.Process.Pid)
			want := cmd.Process.Pid
			if owner == "owned" {
				want = syscall.Getpgrp()
			}
			cancel()
			_ = cmd.Wait()
			if err != nil || group != want {
				t.Fatal("command escaped its actual group owner", group, want, err)
			}
		})
	}
}
