//go:build linux

package updater

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestUpdaterProcessHelper(t *testing.T) {
	if os.Getenv("TEND_UPDATER_TEST_HELPER") != "1" {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	defer signal.Stop(ch)
	<-ch
	os.Exit(0)
}
func TestLinuxProcessPinsStopsAndRecoversExactChild(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Args: []string{"-test.run=^TestUpdaterProcessHelper$"}, LogPath: filepath.Join(t.TempDir(), "child.log")}
	p := LinuxProcess{}
	launchID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.Start(context.Background(), binary, cfg, map[string]string{"TEND_UPDATER_TEST_HELPER": "1", "TEND_UPDATER_LAUNCH_ID": launchID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Stop(ctx, id, time.Second)
	})
	got, err := p.Inspect(context.Background(), id.PID)
	if err != nil || got.StartTicks != id.StartTicks || got.SHA256 != id.SHA256 {
		t.Fatalf("incorrect identity: %+v %v", got, err)
	}
	found, err := p.Find(context.Background(), launchID, id.SHA256)
	if err != nil || len(found) != 1 || found[0].PID != id.PID {
		t.Fatalf("recovery failed: %+v %v", found, err)
	}
	forged := id
	forged.StartTicks++
	if err := p.Stop(context.Background(), forged, time.Second); err == nil {
		t.Fatal("reused PID identity accepted")
	}
	if _, err := p.Inspect(context.Background(), id.PID); err != nil {
		t.Fatal("wrong identity stopped real child")
	}
	if err := p.Stop(context.Background(), id, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Inspect(context.Background(), id.PID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("child still running: %v", err)
	}
}
func TestAtomicWritePreservesOriginalWhenReplacementFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := atomicWrite(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(path, "invalid"), []byte("new"), 0o600); err == nil {
		t.Fatal("invalid destination accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "original" {
		t.Fatal("previous durable state damaged")
	}
}

func TestProcessExitProofDoesNotHideLivePermissionErrors(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		stat   string
		exited bool
	}{{"12 (worker) S 1 2", false}, {"12 (worker) Z 1 2", true}, {"12 (worker) X 1 2", true}, {"bad", false}} {
		if err := os.WriteFile(filepath.Join(root, "stat"), []byte(tt.stat), 0600); err != nil {
			t.Fatal(err)
		}
		if got := processExited(root); got != tt.exited {
			t.Fatalf("stat %q: exit=%v", tt.stat, got)
		}
	}
	if err := os.Remove(filepath.Join(root, "stat")); err != nil {
		t.Fatal(err)
	}
	if !processExited(root) {
		t.Fatal("disappeared process not recognized")
	}
}

func TestBootstrapVerifiesRuntimeAndLaunchIdentityMatch(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"missing", "different", "matching"} {
		t.Run(which, func(t *testing.T) {
			cfg := Config{Args: []string{"-test.run=^TestUpdaterProcessHelper$"}, LogPath: filepath.Join(t.TempDir(), "helper.log")}
			launch, err := newID()
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{"TEND_UPDATER_TEST_HELPER": "1", "TEND_UPDATER_LAUNCH_ID": launch, "TEND_UPDATER_PROCESS_INSTANCE_ID": ""}
			if which == "different" {
				env["TEND_UPDATER_PROCESS_INSTANCE_ID"] = "other"
			}
			if which == "matching" {
				env["TEND_UPDATER_PROCESS_INSTANCE_ID"] = launch
			}
			p := LinuxProcess{}
			id, err := p.Start(context.Background(), binary, cfg, env)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := p.Stop(context.Background(), id, 2*time.Second); err != nil {
					t.Error(err)
				}
			}()
			err = p.VerifyCommand(context.Background(), id.PID, cfg)
			if (err == nil) != (which == "matching") {
				t.Fatalf("incorrect bootstrap identity result: %v", err)
			}
		})
	}
}

func TestSignalledProcessObservationRetriesPermissionErrors(t *testing.T) {
	id := Identity{PID: 123, StartTicks: 7}
	calls := 0
	err := observeSignalledProcess(context.Background(), id, time.Second, func(context.Context, int) (Identity, error) {
		calls++
		if calls <= 2 {
			return Identity{}, os.ErrPermission
		}
		return Identity{}, ErrNotRunning
	})
	if err != nil || calls != 3 {
		t.Fatalf("transient observation abandoned: calls=%d err=%v", calls, err)
	}
}
func TestSignalledProcessUnreadableIsNotExitProof(t *testing.T) {
	err := observeSignalledProcess(context.Background(), Identity{PID: 123, StartTicks: 7}, 5*time.Millisecond, func(context.Context, int) (Identity, error) { return Identity{}, os.ErrPermission })
	if err == nil || errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected bounded stop deadline, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = observeSignalledProcess(ctx, Identity{PID: 123}, time.Second, func(context.Context, int) (Identity, error) { return Identity{}, os.ErrPermission })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
}
