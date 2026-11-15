//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunTaskCancellationStopsItsProcessGroup(t *testing.T) {
	directory := t.TempDir()
	ready, late := filepath.Join(directory, "ready"), filepath.Join(directory, "late")
	script := "#!/bin/sh\n(sleep 1; printf leaked >\"$LATE\") &\nprintf ready >\"$READY\"\nwait\n"
	if err := os.WriteFile(filepath.Join(directory, "make"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runTask(ctx, task{name: "fixture", env: environment(os.Environ(), map[string]string{"READY": ready, "LATE": late})})
	}()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			<-done
			t.Fatal("fixture did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled command did not finish")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatal("owned background process survived cancellation", err)
	}
}

func TestRunTaskCancellationLetsCoordinatorJoinOwnedGroups(t *testing.T) {
	directory := t.TempDir()
	ready, cleaned := filepath.Join(directory, "ready"), filepath.Join(directory, "cleaned")
	script := "#!/bin/sh\ntrap 'printf joined >\"$CLEANED\"; exit 0' TERM\nsleep 120 &\nprintf ready >\"$READY\"\nwait\n"
	if err := os.WriteFile(filepath.Join(directory, "make"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runTask(ctx, task{name: "coordinator", env: environment(os.Environ(), map[string]string{"READY": ready, "CLEANED": cleaned})})
	}()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			<-done
			t.Fatal("fixture did not start")
		case <-poll.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled coordinator succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not finish")
	}
	if data, err := os.ReadFile(cleaned); err != nil || string(data) != "joined" {
		t.Fatal("coordinator could not perform owned cleanup", string(data), err)
	}
}
