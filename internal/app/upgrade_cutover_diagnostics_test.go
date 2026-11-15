package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/logging"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestUpgradeCutoverDecisionRetainsActualHostSnapshot(t *testing.T) {
	for _, condition := range []string{"offline", "heartbeat_stale", "slots_not_positive", "control_mode_not_normal", "runtime_mode_changed", "instance_changed", "missing_runtime", "missing_host", "recovered_offline"} {
		t.Run(condition, func(t *testing.T) {
			// This helper performs Start, both maintenance ACKs, authorization
			// and two replacement-health samples before normal is issued.
			h := serverNormalAckGap(t)
			if condition == "recovered_offline" {
				upgradeAck(h, "linux-2", protocol.RunnerNormal)
				upgradeTick(h, ServerObservation{})
				for _, c := range upgradeRead(h).CutoverRunners {
					if c.HostID == "linux-2" && c.Pending {
						t.Fatal("actual normal ACK was not consumed")
					}
				}
			}
			var out bytes.Buffer
			WithLogger(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))(h.app)
			var before store.HostRuntimeRow
			var control protocol.RunnerControl
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				var err error
				before, err = store.GetHostRuntime(t.Context(), tx.Q(), "linux-2")
				if err != nil {
					return err
				}
				control, err = runnerControl(t.Context(), tx.Q(), "linux-2", h.clock.Now().UnixMilli())
				if err != nil {
					return err
				}
				switch condition {
				case "offline", "recovered_offline":
					before.Online = false
				case "heartbeat_stale":
					before.LastHeartbeatAt = h.clock.Now().Add(-RunnerOfflineAfter).UnixMilli()
				case "slots_not_positive":
					_, err := tx.Exec(t.Context(), `UPDATE hosts SET slots=0 WHERE host_id='linux-2'`)
					return err
				case "missing_host":
					_, err := tx.Exec(t.Context(), `UPDATE hosts SET status='removed' WHERE host_id='linux-2'`)
					return err
				case "missing_runtime":
					_, err := tx.Exec(t.Context(), `DELETE FROM host_runtime WHERE host_id='linux-2'`)
					return err
				case "control_mode_not_normal":
					control.Mode = protocol.RunnerMaintenance
				case "runtime_mode_changed":
					before.Runtime.Mode = protocol.RunnerNormal
				case "instance_changed":
					before.Runtime.InstanceID = "different-runner"
				}
				if err := tx.PutHostControl(t.Context(), "linux-2", control, h.clock.Now().UnixMilli()); err != nil {
					return err
				}
				return tx.PutHostRuntime(t.Context(), "linux-2", before)
			}); err != nil {
				t.Fatal(err)
			}
			original := upgradeRead(h)
			view := upgradeTick(h, ServerObservation{})
			waiting := condition == "offline" || condition == "heartbeat_stale"
			wantStatus, wantLevel, wantDecision, wantOutcome := "attention", "WARN", "rejected", "committed"
			if waiting {
				wantStatus, wantLevel, wantDecision = "running", "DEBUG", "awaiting_normal_ack"
				wantOutcome = "no_effect"
			}
			if view.Status != wantStatus || view.Hosts[0].Status != "queued" || view.Hosts[1].Status != "queued" {
				t.Fatal("diagnostics changed the actual cutover decision or drained a host", view)
			}
			if after := upgradeRead(h); after.Deadline != original.Deadline || after.View.OverallDeadlineMillis != original.View.OverallDeadlineMillis {
				t.Fatal("diagnostics extended the original budget")
			}
			var decision map[string]any
			for _, record := range diagnosticRecords(t, &out) {
				if record["msg"] == "upgrade cutover runner decision" && record["host_id"] == "linux-2" {
					decision = record
				}
			}
			if decision == nil || decision["level"] != wantLevel || decision["decision"] != wantDecision || decision["command_outcome"] != wantOutcome || decision["upgrade_id"] != view.UpgradeID || decision["trace_id"] == "" || decision["request_id"] == "" || decision["stage_deadline_ms"] != float64(original.Deadline) || decision["overall_deadline_ms"] != float64(original.View.OverallDeadlineMillis) {
				t.Fatalf("missing correlated actual cutover snapshot: %+v", decision)
			}
			predicates := map[string]string{
				"slots_not_positive":      "slots_not_positive",
				"control_mode_not_normal": "control_mode_not_normal",
				"runtime_mode_changed":    "maintenance_ack_changed",
				"instance_changed":        "instance_changed",
				"missing_runtime":         "runtime_missing",
				"missing_host":            "host_missing",
				"recovered_offline":       "recovered_host_unavailable",
			}
			if !waiting && decision["failed_predicate"] != predicates[condition] {
				t.Fatal("rejection omitted its actual failed predicate", decision)
			}
			if condition == "missing_host" {
				if _, ok := decision["host_slots"]; ok {
					t.Fatal("missing host slots fabricated", decision)
				}
				return
			}
			if condition == "slots_not_positive" || condition == "missing_runtime" {
				if _, ok := decision["runtime_mode"]; ok {
					t.Fatal("unread runtime fabricated", decision)
				}
				return
			}
			if decision["online"] != before.Online || decision["runtime_mode"] != before.Runtime.Mode || decision["instance_id"] != before.Runtime.InstanceID || decision["applied_control_revision"] != float64(before.Runtime.AppliedControlRevision) || decision["heartbeat_age_ms"] != float64(h.clock.Now().UnixMilli()-before.LastHeartbeatAt) || decision["control_mode"] != control.Mode || decision["control_revision"] != float64(control.Revision) {
				t.Fatal("actual host facts replaced or omitted", decision)
			}
		})
	}
}

func TestUpgradeCutoverDecisionAcknowledgmentKeepsOriginalSnapshot(t *testing.T) {
	h := serverNormalAckGap(t)
	var out bytes.Buffer
	WithLogger(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))(h.app)
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	if r := upgradeRead(h); r.CutoverRunners[1].Pending || !r.CutoverRunners[0].Pending {
		t.Fatal("actual single ACK was not persisted without draining")
	}
	ackCount := func() int {
		n := 0
		for _, record := range diagnosticRecords(t, &out) {
			if record["msg"] == "upgrade cutover runner decision" && record["decision"] == "normal_acknowledged" {
				if record["command_outcome"] != "committed" || record["control_mode"] != protocol.RunnerNormal || record["online"] != true || record["applied_control_revision"] != record["expected_normal_revision"] {
					t.Fatal("normal ACK diagnostic lacks actual committed facts", record)
				}
				n++
			}
		}
		return n
	}
	if ackCount() != 1 {
		t.Fatal("first actual normal ACK was not observed exactly once", out.String())
	}
	upgradeTick(h, ServerObservation{})
	if ackCount() != 1 {
		t.Fatal("already consumed ACK was logged as a new transition")
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	if view := upgradeTick(h, ServerObservation{}); view.Hosts[0].Phase != "draining" || view.Hosts[1].Status != "queued" {
		t.Fatal("actual complete ACKs did not authorize only the first drain", view)
	}
	if ackCount() != 2 {
		t.Fatal("second actual normal ACK lost its pre-drain snapshot", out.String())
	}
}

func TestUpgradeCutoverDecisionUsesProductionTextHandler(t *testing.T) {
	h := serverNormalAckGap(t)
	var out bytes.Buffer
	logger, err := logging.NewText(&out, "info")
	if err != nil {
		t.Fatal(err)
	}
	WithLogger(logger)(h.app)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		rt, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-2")
		if err != nil {
			return err
		}
		rt.Runtime.Mode = protocol.RunnerNormal
		return tx.PutHostRuntime(t.Context(), "linux-2", rt)
	}); err != nil {
		t.Fatal(err)
	}
	if view := upgradeTick(h, ServerObservation{}); view.Status != "attention" || view.Hosts[0].Status != "queued" {
		t.Fatal("changed observation did not pause before drain", view)
	}
	for _, field := range []string{"upgrade cutover runner decision", "decision=rejected", "failed_predicate=", "host_id=linux-2", "heartbeat_age_ms=", "applied_control_revision=", "expected_normal_revision=", "stage_deadline_ms="} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("production text handler omitted %q: %s", field, out.String())
		}
	}
}
