package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCapabilityHandoffUsesReviewedInstallerAndRejectsDigestDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "installer")
	body := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
	if err := os.WriteFile(path, body, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	for _, action := range []string{"plan", "apply", "status"} {
		env := newTestEnv(t, "")
		args := []string{"updater", "handoff", action, "--installer", path, "--installer-sha256", hex.EncodeToString(sum[:]), "--config", filepath.Join(root, "fixed.json"), "--plan", filepath.Join(root, "plan.json"), "--json"}
		if code := env.main(args...); code != exitOK {
			t.Fatal(code, env.errOut)
		}
		if !strings.Contains(env.out.String(), "handoff\n"+action+"\n--config\n") {
			t.Fatal("wrong first-hop invocation", env.out.String())
		}
		env.out.Reset()
		env.errOut.Reset()
		if action != "status" {
			resumed := append(append([]string(nil), args...), "--resume-request-id", "req_01M6F01JSS6Q9BFV6DJNSSBHEY", "--expected-generation", "4")
			if code := env.main(resumed...); code != exitOK || !strings.Contains(env.out.String(), "--resume-request-id\nreq_01M6F01JSS6Q9BFV6DJNSSBHEY\n--expected-generation\n4") {
				t.Fatal("continuation input not forwarded", code, env.out, env.errOut)
			}
			env.out.Reset()
			env.errOut.Reset()
		}
		for i, a := range args {
			if a == "--installer-sha256" {
				args[i+1] = strings.Repeat("0", 64)
			}
		}
		if code := env.main(args...); code != exitUsage || env.out.Len() != 0 {
			t.Fatal("drifted installer was executed", code, env.out.String())
		}
	}
}

func TestCapabilityRecoveryHelpListsCompleteInputs(t *testing.T) {
	for _, command := range [][]string{{"updater", "handoff", "apply"}, {"upgrade", "recover", "apply"}} {
		env := newTestEnv(t, "")
		args := append(append([]string(nil), command...), "--help")
		if code := env.main(args...); code != exitOK {
			t.Fatal(code, env.errOut)
		}
		for _, flag := range []string{"plan", "expected-generation"} {
			if !strings.Contains(env.out.String(), flag) {
				t.Fatal("missing recovery help input", flag, env.out)
			}
		}
	}
}
