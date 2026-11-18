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

func TestMaintenanceCaseAndScheduledAuditKeepOriginalCLIInput(t *testing.T) {
	subject := protocol.MaintenanceSubject{IssueID: "issue-1", TaskID: "owner-task", RunID: "original", WaitID: "wait", Evidence: "original result event", Expected: "owner disposition"}
	raw, err := json.Marshal(subject)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		action string
		args   []string
		check  func(protocol.MaintenanceRequest) bool
	}{
		{"report", []string{"--id", "missing_notice", "--reason", "observed source missing", "--data", string(raw)}, func(r protocol.MaintenanceRequest) bool {
			return r.Subject != nil && *r.Subject == subject && r.CaseID == "missing_notice"
		}},
		{"intervene", []string{"--id", "case-1", "--reason", "original receipt"}, func(r protocol.MaintenanceRequest) bool {
			return r.CaseID == "case-1" && r.Reason == "original receipt"
		}},
		{"resolve", []string{"--id", "case-1", "--reason", "complete", "--verification", "owner turn"}, func(r protocol.MaintenanceRequest) bool { return r.Verification == "owner turn" }},
		{"audit", []string{"--scheduled"}, func(r protocol.MaintenanceRequest) bool { return r.ScheduledAudit }},
	} {
		t.Run(tt.action, func(t *testing.T) {
			te := newTestEnv(t, "")
			args := append([]string{tt.action, "--socket", "/unused.sock", "--revision", "1", "--request-id", "original", "--dry-run"}, tt.args...)
			if err := runMaintenance(t.Context(), te.Env, args); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Request protocol.MaintenanceRequest `json:"request"`
			}
			if err := json.Unmarshal(te.out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Request.RequestID != "original" || got.Request.ExpectedRevision != 1 || !tt.check(got.Request) {
				t.Fatal(got)
			}
		})
	}
}
