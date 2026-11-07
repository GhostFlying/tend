//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnedLaunchExitSurvivesOrdinaryStartAndPreventsReplay(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sha, err := hashFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{InstanceID: "owned-launch-test", StateDir: ownedLaunchTestDirectory(t), Args: []string{"-test.run=^TestUpdaterProcessHelper$"}, LogPath: filepath.Join(t.TempDir(), "child.log")}
	extra := map[string]string{"TEND_UPDATER_TEST_HELPER": "1", "TEND_UPDATER_LAUNCH_ID": "ordinary-owned-launch", "TEND_RELEASE_SHA256": sha}
	p := LinuxProcess{}
	id, err := p.Start(t.Context(), binary, cfg, extra)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = p.Stop(ctx, id, time.Second)
	})
	if proof, err := LoadOwnedLaunchExit(cfg, extra["TEND_UPDATER_LAUNCH_ID"], sha); err != nil || proof != nil {
		t.Fatalf("live owned child was treated as exited: %+v %v", proof, err)
	}
	if duplicate, err := p.Start(t.Context(), binary, cfg, extra); !errors.Is(err, ErrUnknown) {
		if duplicate.PID != 0 {
			_ = p.Stop(t.Context(), duplicate, time.Second)
		}
		t.Fatalf("original dispatched launch was replayable: %v", err)
	}
	if err := p.Stop(t.Context(), id, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	proof := awaitOwnedLaunchExit(t, cfg, extra["TEND_UPDATER_LAUNCH_ID"], sha)
	if proof.PID != id.PID || proof.LaunchID != id.LaunchID || proof.ArtifactSHA256 != id.SHA256 {
		t.Fatalf("wait result lost its original binding: %+v / %+v", proof, id)
	}
	var exited *launchExitedError
	if _, err := p.Start(t.Context(), binary, cfg, extra); !errors.As(err, &exited) || exited.proof != *proof {
		t.Fatalf("terminal original launch was replayed: %+v %v", exited, err)
	}
	changed := cfg
	changed.Args = []string{"different-execution"}
	if _, err := LoadOwnedLaunchExit(changed, proof.LaunchID, sha); err == nil {
		t.Fatal("exit proof accepted for a changed execution configuration")
	}
}

func TestOwnedLaunchFastExitIsDurableWithoutInspectIdentity(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sha, err := hashFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, maintenance := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "maintenance"}[maintenance], func(t *testing.T) {
			cfg := Config{InstanceID: "fast-exit-test", StateDir: ownedLaunchTestDirectory(t), Args: []string{"-test.run=^TestUpdaterMaintenanceFastExitHelper$"}, LogPath: filepath.Join(t.TempDir(), "child.log")}
			extra := map[string]string{"TEND_UPDATER_FAST_EXIT": "1", "TEND_UPDATER_LAUNCH_ID": "durable-fast-exit", "TEND_RELEASE_SHA256": sha}
			start := (LinuxProcess{}).Start
			if maintenance {
				start = (LinuxProcess{}).StartMaintenance
			}
			id, startErr := start(t.Context(), binary, cfg, extra)
			if startErr != nil && !errors.Is(startErr, ErrUnknown) {
				t.Fatal(startErr)
			}
			proof := awaitOwnedLaunchExit(t, cfg, extra["TEND_UPDATER_LAUNCH_ID"], sha)
			if proof.ExitCode != 17 || proof.Signal != 0 || proof.PID <= 1 || (id.PID != 0 && proof.PID != id.PID) {
				t.Fatalf("invalid owned fast-exit proof: %+v, identity %+v", proof, id)
			}
		})
	}
}

func TestOwnedLaunchMissingReceiptDoesNotProveExit(t *testing.T) {
	cfg := Config{StateDir: ownedLaunchTestDirectory(t)}
	if proof, err := LoadOwnedLaunchExit(cfg, "missing-launch", strings.Repeat("a", 64)); err != nil || proof != nil {
		t.Fatalf("missing launch became exit proof: %+v %v", proof, err)
	}
}

func TestOwnedLaunchDefiniteStartFailureRetainsIntentAndAllowsRetry(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(candidate, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sha := bytesDigest(raw)
	cfg := Config{StateDir: ownedLaunchTestDirectory(t), Args: []string{"-test.run=^TestUpdaterMaintenanceFastExitHelper$"}, LogPath: filepath.Join(t.TempDir(), "child.log")}
	extra := map[string]string{"TEND_UPDATER_FAST_EXIT": "1", "TEND_UPDATER_LAUNCH_ID": "unstarted-owned-launch", "TEND_RELEASE_SHA256": sha}
	if _, err := (LinuxProcess{}).Start(t.Context(), candidate, cfg, extra); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("definite exec rejection lost its safe pre-child classification: %v", err)
	}
	dir, err := ownedLaunchDirectory(cfg, extra["TEND_UPDATER_LAUNCH_ID"], false)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := privateFile(filepath.Join(dir, "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "dispatch.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("definite exec rejection retained an uncertain dispatch: %v", err)
	}
	if err := os.Chmod(candidate, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := (LinuxProcess{}).Start(t.Context(), candidate, cfg, extra); err != nil && !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	proof := awaitOwnedLaunchExit(t, cfg, extra["TEND_UPDATER_LAUNCH_ID"], sha)
	if proof.ExitCode != 17 {
		t.Fatal("retry did not own the actual child exit", proof)
	}
	after, err := privateFile(filepath.Join(dir, "intent.json"))
	if err != nil || string(intent) != string(after) {
		t.Fatal("safe retry replaced its original intent", err)
	}
}

func TestOwnedLaunchExitRejectsChangedFacts(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sha, err := hashFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateDir: ownedLaunchTestDirectory(t), Args: []string{"-test.run=^TestUpdaterMaintenanceFastExitHelper$"}, LogPath: filepath.Join(t.TempDir(), "child.log")}
	launch := "immutable-owned-exit"
	if _, err := (LinuxProcess{}).Start(t.Context(), binary, cfg, map[string]string{"TEND_UPDATER_FAST_EXIT": "1", "TEND_UPDATER_LAUNCH_ID": launch, "TEND_RELEASE_SHA256": sha}); err != nil && !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	_ = awaitOwnedLaunchExit(t, cfg, launch, sha)
	dir, err := ownedLaunchDirectory(cfg, launch, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dispatch.json", "child.json", "exit.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			raw, err := privateFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var changed map[string]any
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			if name == "dispatch.json" {
				changed["nonce"] = "different-owner"
			} else if name == "child.json" {
				changed["pid"] = float64(2)
			} else {
				changed["proof"].(map[string]any)["pid"] = float64(2)
			}
			bad, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, bad, 0o600); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Error(err)
				}
			}()
			if proof, err := LoadOwnedLaunchExit(cfg, launch, sha); err == nil || proof != nil {
				t.Fatalf("changed fact accepted: %+v %v", proof, err)
			}
		})
	}
	if proof, err := LoadOwnedLaunchExit(cfg, launch, strings.Repeat("0", 64)); err == nil || proof != nil {
		t.Fatalf("requested digest was substituted for verified FD digest: %+v %v", proof, err)
	}
}

func ownedLaunchTestDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func awaitOwnedLaunchExit(t *testing.T, cfg Config, launch, sha string) *LaunchExitProof {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		proof, err := LoadOwnedLaunchExit(cfg, launch, sha)
		if err != nil {
			t.Fatal(err)
		}
		if proof != nil {
			return proof
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("owned Wait did not publish its exit receipt within the fixture budget")
	return nil
}
