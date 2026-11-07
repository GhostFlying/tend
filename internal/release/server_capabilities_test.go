package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestManagerParkServerCapabilitiesBindActualExecutable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel executable identity")
	}
	report, err := CurrentServerCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if err := report.Validate(hex.EncodeToString(sum[:])); err != nil || !report.Supports(ServerCapabilityManagerPark) || !report.Supports(ServerCapabilityOperationRecovery) {
		t.Fatal(report, err)
	}
	report.Capabilities = append(report.Capabilities, ServerCapabilityManagerPark)
	if report.Validate(hex.EncodeToString(sum[:])) == nil {
		t.Fatal("duplicate capability accepted")
	}
}

func TestManagerParkServerCapabilityProbeFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local executable fixture")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "candidate")
	cases := []struct {
		name, body string
		timeout    bool
	}{
		{"old-binary", "#!/bin/sh\nexit 2\n", false},
		{"malformed", "#!/bin/sh\nprintf 'not-json'\n", false},
		{"wrong-executable", "#!/bin/sh\nprintf '{\"binary_sha256\":\"wrong\",\"capabilities\":[]}'\n", false},
		{"cancelled", "#!/bin/sh\nwhile :; do :; done\n", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			if err := os.WriteFile(path, body, 0o700); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			ctx := t.Context()
			if tt.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			if _, err := ProbeServerCapabilities(ctx, path, hex.EncodeToString(sum[:])); err == nil {
				t.Fatal("unproved capability accepted")
			}
		})
	}
	// Reject hash drift before executing any bytes, including the old binary's
	// normal-start fallback. A capability failure never tries another command.
	marker := filepath.Join(dir, "ordinary-start")
	body := []byte("#!/bin/sh\nprintf started > '" + marker + "'\n")
	if err := os.WriteFile(path, body, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeServerCapabilities(t.Context(), path, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("drift accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unverified candidate executed", err)
	}
}
