//go:build linux

package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func syntheticClosedDiagnostic(t *testing.T, h *harness, id string, age time.Duration, keepWriter bool) *os.File {
	t.Helper()
	sha := h.s.Status().Current.Artifact.SHA256
	file, err := openDiagnosticWriter(h.cfg, id, sha)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(strings.Repeat("diagnostic 汉字🙂\n", 1000)); err != nil {
		t.Fatal(err)
	}
	if err = file.Sync(); err != nil {
		t.Fatal(err)
	}
	path, _, _ := diagnosticLogPaths(h.cfg, id)
	at := h.now.Add(-age)
	if err = os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	receipt, err := prepareOwnedLaunch(h.cfg, id, sha, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = receipt.recordChild(12345); err != nil {
		t.Fatal(err)
	}
	if err = receipt.recordExit(LaunchExitProof{PID: 12345, LaunchID: id, ArtifactSHA256: sha, ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	if !keepWriter {
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	return file
}

func TestDiagnosticInheritedWriterLockSurvivesParentFDClosure(t *testing.T) {
	if os.Getenv("TEND_DIAGNOSTIC_FD_HELPER") == "1" {
		// This helper inherits the exact stdout/stderr open file description.
		_, _ = os.Stdout.WriteString("held inherited writer\n")
		input := make([]byte, 1)
		_, _ = os.Stdin.Read(input)
		os.Exit(0)
	}
	h := newHarness(t)
	id := "inherited-log"
	path, _, _ := diagnosticLogPaths(h.cfg, id)
	writer, err := openDiagnosticWriter(h.cfg, id, h.s.Status().Current.Artifact.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), binary, "-test.run=^TestDiagnosticInheritedWriterLockSurvivesParentFDClosure$")
	child.Env = append(os.Environ(), "TEND_DIAGNOSTIC_FD_HELPER=1")
	child.Stdout = writer
	child.Stderr = writer
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if lock, err := lockClosedDiagnostic(path); !errors.Is(err, errDiagnosticWriterActive) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatal("inherited active FD was not protected", err)
	}
	if err = input.Close(); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatal(err)
	}
	lock, err := lockClosedDiagnostic(path)
	if err != nil {
		t.Fatal("closed inherited FD remains held", err)
	}
	_ = lock.Close()
}

func TestDiagnosticRetentionSealedCompressionProtectionAndResume(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	held := syntheticClosedDiagnostic(t, h, "held", 40*24*time.Hour, true)
	defer held.Close()
	syntheticClosedDiagnostic(t, h, "compress", 8*24*time.Hour, false)
	syntheticClosedDiagnostic(t, h, "delete", 40*24*time.Hour, false)
	syntheticClosedDiagnostic(t, h, "investigation", 40*24*time.Hour, false)
	_, err := h.s.ProtectDiagnosticLog(t.Context(), protocol.DiagnosticLogProtection{LogID: "investigation", ExpectedGeneration: h.s.Status().Generation, Reason: "original incident evidence"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := h.s.BackupPreview(t.Context(), minimalRetention())
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[string]string{}
	for _, log := range preview.DiagnosticLogs {
		decisions[log.ID] = log.Decision
	}
	if decisions["held"] != "protected" || decisions["compress"] != "compress" || decisions["delete"] != "delete" || decisions["investigation"] != "protected" {
		t.Fatal(decisions)
	}
	// Simulate gzip publication before the metadata/progress commit.
	var compress protocol.DiagnosticLog
	for _, r := range preview.DiagnosticLogs {
		if r.ID == "compress" {
			compress = r
		}
	}
	source, err := lockClosedDiagnostic(compress.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = compressDiagnostic(t.Context(), source, compress.Path+".gz", compress.BodySHA256, compress.Bytes); err != nil {
		t.Fatal(err)
	}
	_ = source.Close()
	result, err := h.s.PruneBackups(t.Context(), protocol.BackupPruneRequest{Preview: preview})
	if err != nil || result.Phase != "completed" {
		t.Fatal("sealed logs could not reconcile", result, err)
	}
	if _, err = os.Lstat(compress.Path); !os.IsNotExist(err) {
		t.Fatal("compressed source remains", err)
	}
	if err = verifyDiagnosticGzip(t.Context(), compress.Path+".gz", compress.BodySHA256, compress.Bytes); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"held", "investigation"} {
		path, _, _ := diagnosticLogPaths(h.cfg, id)
		if _, err = os.Stat(path); err != nil {
			t.Fatal("protected log lost", id, err)
		}
	}
	deletePath, deleteManifest, _ := diagnosticLogPaths(h.cfg, "delete")
	for _, path := range []string{deletePath, deleteManifest} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("expired sealed log remains", path, err)
		}
	}
	if again, err := h.s.PruneBackups(t.Context(), protocol.BackupPruneRequest{Preview: preview}); err != nil || again.ReclaimedBytes != result.ReclaimedBytes {
		t.Fatal("retry duplicated log reclaim", again, err)
	}
}

func TestDiagnosticActiveAppendDoesNotInvalidateUnrelatedPreview(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	syntheticClosedDiagnostic(t, h, "old", 40*24*time.Hour, false)
	writer, err := openDiagnosticWriter(h.cfg, "running", h.s.Status().Current.Artifact.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	before, err := h.s.BackupPreview(t.Context(), minimalRetention())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.WriteString("new active diagnostics"); err != nil {
		t.Fatal(err)
	}
	after, err := h.s.BackupPreview(t.Context(), minimalRetention())
	if err != nil || before.SHA256 != after.SHA256 {
		t.Fatal("active append blocked unrelated closed log cleanup", before.SHA256, after.SHA256, err)
	}
}

func TestDiagnosticLegacyLogAndUnknownExitRemainProtected(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.cfg.LogPath, []byte("unique legacy incident"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openDiagnosticWriter(h.cfg, "unknown", h.s.Status().Current.Artifact.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	logs, err := h.s.diagnosticLogPreview(context.Background(), DefaultBackupPolicy())
	if err != nil || len(logs) != 2 {
		t.Fatal(logs, err)
	}
	for _, log := range logs {
		if log.Decision != "protected" {
			t.Fatal("unknown legacy log became collectible", log)
		}
	}
	raw, err := os.ReadFile(h.cfg.LogPath)
	if err != nil || string(raw) != "unique legacy incident" {
		t.Fatal("legacy inode overwritten", err)
	}
}

func TestDiagnosticActualControlledLaunchUsesGenerationSegment(t *testing.T) {
	if os.Getenv("TEND_DIAGNOSTIC_OWNED_HELPER") == "1" {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("TEND_DIAGNOSTIC_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(2)
		}
		<-stop
		os.Exit(0)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sha, err := hashFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{InstanceID: "log-launch", StateDir: ownedLaunchTestDirectory(t), Args: []string{"-test.run=^TestDiagnosticActualControlledLaunchUsesGenerationSegment$"}, LogPath: filepath.Join(t.TempDir(), "current.log")}
	launch := "actual-log-" + strconv.Itoa(os.Getpid())
	process := LinuxProcess{}
	ready := filepath.Join(t.TempDir(), "ready")
	identity, err := process.Start(t.Context(), binary, cfg, map[string]string{"TEND_DIAGNOSTIC_OWNED_HELPER": "1", "TEND_DIAGNOSTIC_READY": ready, "TEND_UPDATER_LAUNCH_ID": launch, "TEND_RELEASE_SHA256": sha})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Stop(context.Background(), identity, 3*time.Second) })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual log writer never became ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	path, _, _ := diagnosticLogPaths(cfg, launch)
	if _, err = lockClosedDiagnostic(path); !errors.Is(err, errDiagnosticWriterActive) {
		t.Fatal("actual child did not hold writer FD", err)
	}
	if err = process.Stop(t.Context(), identity, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	awaitOwnedLaunchExit(t, cfg, launch, sha)
	lock, err := lockClosedDiagnostic(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	if target, err := os.Readlink(cfg.LogPath); err != nil || target != path {
		t.Fatal("current link does not identify actual generation", target, err)
	}
}
