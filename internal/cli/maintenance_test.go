package cli

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"path/filepath"
	"testing"
)

func TestMaintenancePlanResolvesCallerReleasePath(t *testing.T) {
	te := newTestEnv(t, "")
	err := runMaintenance(t.Context(), te.Env, []string{"upgrade-plan", "--release", "./release-component", "--socket", "/unused.sock", "--revision", "1", "--request-id", "original", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Request protocol.MaintenanceRequest `json:"request"`
	}
	if err := json.Unmarshal(te.out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("./release-component")
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.ReleaseDir != want {
		t.Fatalf("release path is sent relative to daemon instead of caller cwd: %q", got.Request.ReleaseDir)
	}
}
