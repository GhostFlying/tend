//go:build linux

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBoundedInstallerDoesNotWaitForeverForInheritedOutput(t *testing.T) {
	root := t.TempDir()
	body := []byte("#!/bin/sh\nsleep 30 &\nprintf '%s\\n' \"$!\"\n")
	installer := filepath.Join(root, "installer")
	if err := os.WriteFile(installer, body, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	e := newTestEnv(t, "")
	start := time.Now()
	err := runPinnedBoundedInstaller(t.Context(), e.Env, boundedInstallerInvocation{
		Installer: installer, InstallerSHA256: hex.EncodeToString(sum[:]),
		Action: "takeover", Mutating: true, Timeout: time.Second,
		RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY",
	})
	var result struct {
		Outcome         string `json:"outcome"`
		InstallerOutput string `json:"installer_output"`
	}
	if decodeErr := json.Unmarshal(e.out.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr, e.out.String())
	}
	var pid int
	if _, scanErr := fmt.Sscan(result.InstallerOutput, &pid); scanErr != nil || pid <= 0 {
		t.Fatal("missing descendant identity", scanErr, result.InstallerOutput)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err == nil || time.Since(start) > 2*time.Second || result.Outcome != "unknown" {
		t.Fatalf("inherited stdout kept installer wait alive: %v, %s, %+v", err, time.Since(start), result)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("installer exit was confused with descendant exit: %v", err)
	}
}
