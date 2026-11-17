package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExternalRecoveryInvokesOnlyThePinnedNewInstaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	root := t.TempDir()
	installer := filepath.Join(root, "updater")
	script := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
	if err := os.WriteFile(installer, script, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(script)
	set := map[string]any{"format": 2, "artifacts": []map[string]string{{"role": "updater", "os": runtime.GOOS, "arch": runtime.GOARCH, "path": "updater", "sha256": hex.EncodeToString(sum[:])}}}
	raw, _ := json.Marshal(set)
	if err := os.WriteFile(filepath.Join(root, "release-set.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"--force", "--status"} {
		env := newTestEnv(t, "")
		args := []string{"upgrade", "recover", "--release", root, "--config", filepath.Join(root, "fixed.json"), "--request-id", "original-recovery", mode}
		if code := env.main(args...); code != exitOK {
			t.Fatal(code, env.errOut)
		}
		if !strings.Contains(env.out.String(), "external-recover\n--release\n"+root) || !strings.Contains(env.out.String(), "--request-id\noriginal-recovery\n"+mode) {
			t.Fatal(env.out)
		}
	}
	if err := os.WriteFile(installer, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, "")
	if code := env.main("upgrade", "recover", "--release", root, "--config", filepath.Join(root, "fixed.json"), "--force"); code != exitUsage || env.out.Len() != 0 {
		t.Fatal("changed executable ran", code, env.out, env.errOut)
	}
}
