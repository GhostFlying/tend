//go:build !windows

package impact

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestImpactOrchestratorHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	mode, record := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if mode == "leaf" {
		if err := os.WriteFile(record, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		return
	}
	next := "inner"
	if mode == "inner" {
		next = "leaf"
	}
	call := CommandGraceful
	if mode == "inner" {
		call = Command
	}
	err := call(ctx, "", io.Discard, nil, os.Args[0], "-test.run=^TestImpactOrchestratorHelper$", "--", next, record)
	if ctx.Err() == nil {
		t.Fatalf("unexpected early child exit: %v", err)
	}
}

func TestNestedOrchestratorCancellationCleansChildGroups(t *testing.T) {
	record := t.TempDir() + "/leaf"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- CommandGraceful(ctx, "", io.Discard, nil, os.Args[0], "-test.run=^TestImpactOrchestratorHelper$", "--", "outer", record)
	}()
	deadline := time.Now().Add(15 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(record); err == nil {
			// WriteFile creates the marker before writing its PID. Observing
			// that empty intermediate file is not a readiness notification.
			if candidate, err := strconv.Atoi(string(b)); err == nil && candidate > 0 {
				pid = candidate
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("nested leaf never became ready")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("orchestrator did not stop")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("leaf process survived cancellation: %v", err)
	}
}
