package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBoundedInstallerTimeoutPreservesUnknownRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "attempt")
	body := []byte("#!/bin/sh\nprintf 'started\\n'\nprintf 'effect\\n' >> \"$1\"\nexec sleep 30\n")
	installer := filepath.Join(root, "installer")
	if err := os.WriteFile(installer, body, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	e := newTestEnv(t, "")
	start := time.Now()
	err := runPinnedBoundedInstaller(t.Context(), e.Env, boundedInstallerInvocation{
		Installer: installer, InstallerSHA256: hex.EncodeToString(sum[:]),
		Arguments: []string{marker}, Action: "force", Mutating: true,
		RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", ExpectedGeneration: 17,
		ExpectedPhase: "draining", Timeout: 80 * time.Millisecond,
		NextCommand: []string{"tend", "updater", "bounded", "reconcile"},
	})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("installer wait was not bounded: %v, elapsed %s", err, time.Since(start))
	}
	var result struct {
		Outcome            string   `json:"outcome"`
		RequestID          string   `json:"request_id"`
		ExpectedGeneration uint64   `json:"expected_generation"`
		ExpectedPhase      string   `json:"expected_phase"`
		InstallerOutput    string   `json:"installer_output"`
		NextCommand        []string `json:"next_command"`
	}
	if err := json.Unmarshal(e.out.Bytes(), &result); err != nil {
		t.Fatal(err, e.out.String())
	}
	if result.Outcome != "unknown" || result.RequestID != "req_01M6F01JSS6Q9BFV6DJNSSBHEY" || result.ExpectedGeneration != 17 || result.ExpectedPhase != "draining" || !strings.Contains(result.InstallerOutput, "started") || len(result.NextCommand) == 0 {
		t.Fatalf("timeout lost the original recovery identity: %+v", result)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "effect\n" {
		t.Fatalf("command was not attempted exactly once: %q, %v", got, err)
	}
}

func TestBoundedInstallerRejectsUnreviewedArtifactBeforeExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	root := t.TempDir()
	installer := filepath.Join(root, "installer")
	if err := os.WriteFile(installer, []byte("#!/bin/sh\nprintf 'unreviewed\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, "")
	err := runPinnedBoundedInstaller(context.Background(), e.Env, boundedInstallerInvocation{Installer: installer, InstallerSHA256: strings.Repeat("0", 64), Action: "status", Timeout: time.Second})
	if err == nil || e.out.Len() != 0 {
		t.Fatalf("unreviewed installer executed: %v, %s", err, e.out.String())
	}
}

func TestBoundedInstallerUnexpectedExitKeepsOriginalUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	root := t.TempDir()
	body := []byte("#!/bin/sh\nprintf 'effect started\\n'\nkill -9 \"$$\"\n")
	installer := filepath.Join(root, "installer")
	if err := os.WriteFile(installer, body, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	e := newTestEnv(t, "")
	err := runPinnedBoundedInstaller(t.Context(), e.Env, boundedInstallerInvocation{
		Installer: installer, InstallerSHA256: hex.EncodeToString(sum[:]),
		Action: "force", Mutating: true, RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", Timeout: time.Second,
	})
	var result struct {
		Outcome         string `json:"outcome"`
		RequestID       string `json:"request_id"`
		InstallerOutput string `json:"installer_output"`
	}
	if decodeErr := json.Unmarshal(e.out.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr, e.out.String())
	}
	if err == nil || result.Outcome != "unknown" || result.RequestID == "" || !strings.Contains(result.InstallerOutput, "effect started") {
		t.Fatalf("installer death erased original uncertain work: %+v, %v", result, err)
	}
}
