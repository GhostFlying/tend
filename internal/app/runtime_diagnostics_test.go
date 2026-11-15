package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func diagnosticRecords(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

// Ordinary availability diagnostics also apply to persisted batches whose
// authorization predates cutover cohorts. Keep that historical fact explicit
// rather than mistaking a new batch's first-ACK wait for a later host outage.
func ordinaryServerNormalAckGap(t *testing.T) *harness {
	t.Helper()
	h := serverNormalAckGap(t)
	r := upgradeRead(h)
	r.CutoverRunners = nil
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutUpgrade(t.Context(), r)
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestUpgradeUnavailableDecisionRetainsOriginalHostSnapshot(t *testing.T) {
	for _, condition := range []string{"offline", "heartbeat_stale", "slots_not_positive", "control_mode_not_normal", "runtime_mode_not_normal"} {
		t.Run(condition, func(t *testing.T) {
			h := ordinaryServerNormalAckGap(t)
			var out bytes.Buffer
			WithLogger(slog.New(slog.NewJSONHandler(&out, nil)))(h.app)
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
				case "offline":
					before.Online = false
				case "heartbeat_stale":
					before.LastHeartbeatAt = h.clock.Now().Add(-RunnerOfflineAfter).UnixMilli()
				case "slots_not_positive":
					if _, err := tx.Exec(t.Context(), `UPDATE hosts SET slots=0 WHERE host_id='linux-2'`); err != nil {
						return err
					}
				case "control_mode_not_normal":
					control.Mode = protocol.RunnerMaintenance
				case "runtime_mode_not_normal":
					before.Runtime.Mode = protocol.RunnerMaintenance
					before.Runtime.AppliedControlRevision = control.Revision
				}
				if err := tx.PutHostControl(t.Context(), "linux-2", control, h.clock.Now().UnixMilli()); err != nil {
					return err
				}
				return tx.PutHostRuntime(t.Context(), "linux-2", before)
			}); err != nil {
				t.Fatal(err)
			}
			deadline := upgradeRead(h).Deadline
			v := upgradeTick(h, ServerObservation{})
			if v.Status != "attention" || upgradeRead(h).Deadline != deadline {
				t.Fatal("diagnostics changed the existing pause or deadline", v)
			}
			var decision map[string]any
			for _, record := range diagnosticRecords(t, &out) {
				if record["msg"] == "upgrade host availability decision" {
					decision = record
				}
			}
			if decision == nil || decision["level"] != "WARN" || decision["decision"] != "other_hosts_unavailable" || decision["command_outcome"] != "committed" || decision["target_host_id"] != "linux-1" || decision["upgrade_id"] != v.UpgradeID || decision["request_id"] == "" || decision["trace_id"] == "" {
				t.Fatalf("missing correlated pause snapshot: %+v", decision)
			}
			hosts := decision["other_hosts"].([]any)
			if len(hosts) != 1 {
				t.Fatal(hosts)
			}
			host := hosts[0].(map[string]any)
			predicates := host["failed_predicates"].([]any)
			if host["host_id"] != "linux-2" || !slices.Contains(predicates, any(condition)) {
				t.Fatalf("wrong predicate %s: %+v", condition, host)
			}
			if condition == "slots_not_positive" {
				if host["slots"] != float64(0) || host["runtime"] != nil || host["control"] != nil {
					t.Fatal("short-circuited values fabricated as observed", host)
				}
				return
			}
			runtime := host["runtime"].(map[string]any)
			ctl := host["control"].(map[string]any)
			if runtime["online"] != before.Online || runtime["mode"] != before.Runtime.Mode || runtime["upgrade_id"] != before.Runtime.UpgradeID || runtime["applied_control_revision"] != float64(before.Runtime.AppliedControlRevision) || runtime["heartbeat_age_ms"] != float64(h.clock.Now().UnixMilli()-before.LastHeartbeatAt) || runtime["heartbeat_limit_ms"] != float64(RunnerOfflineAfter.Milliseconds()) || ctl["revision"] != float64(control.Revision) || ctl["mode"] != control.Mode {
				t.Fatal("post-restoration values replaced original decision facts", host)
			}
		})
	}
}

func TestUpgradeNormalAckDecisionIsDebugAndDoesNotPause(t *testing.T) {
	h := ordinaryServerNormalAckGap(t)
	var out bytes.Buffer
	WithLogger(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))(h.app)
	deadline := upgradeRead(h).Deadline
	if v := upgradeTick(h, ServerObservation{}); v.Status != "running" || upgradeRead(h).Deadline != deadline {
		t.Fatal("normal ACK gap changed", v)
	}
	for _, record := range diagnosticRecords(t, &out) {
		if record["msg"] == "upgrade host availability decision" {
			if record["level"] != "DEBUG" || record["decision"] != "awaiting_normal_ack" {
				t.Fatal(record)
			}
			return
		}
	}
	t.Fatal("normal ACK wait lacks bounded decision observation")
}

func TestAppDiagnosticsRespectCommitAndExcludeInput(t *testing.T) {
	h := newHarness(t)
	var out bytes.Buffer
	WithLogger(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))(h.app)
	const privateText = "private-user-payload-and-reason"
	op, task := h.createTask(owner, createTaskPayload{Title: privateText, Spec: testSpec})
	if op.Error != nil {
		t.Fatal(op)
	}
	found := false
	for _, record := range diagnosticRecords(t, &out) {
		if record["msg"] == "app state transition" && record["object_id"] == task.TaskID {
			found = record["command_outcome"] == "committed" && record["trace_id"] != "" && record["request_id"] == op.RequestID
		}
	}
	if !found || strings.Contains(out.String(), privateText) {
		t.Fatal("committed event missing or payload leaked", out.String())
	}
	out.Reset()
	env := h.env("tasks.create", taskTarget(h.ids.New(protocol.PrefixTask)), createTaskPayload{Spec: testSpec}, nil)
	env.Reason = privateText
	if rejected := h.exec(owner, env, CreateTask{}); rejected.Error == nil {
		t.Fatal("invalid real command unexpectedly accepted")
	}
	records := diagnosticRecords(t, &out)
	if len(records) != 1 || records[0]["msg"] != "app command rejected" || records[0]["code"] != string(protocol.CodeInvalidRequest) || records[0]["command_outcome"] != "rejected" || strings.Contains(out.String(), privateText) {
		t.Fatal("rejection diagnostic missing or input leaked", out.String())
	}
	out.Reset()
	before := h.count("tasks")
	env = h.env("tasks.create", taskTarget(h.ids.New(protocol.PrefixTask)), createTaskPayload{Title: privateText, Spec: testSpec}, nil)
	if _, err := h.app.Execute(t.Context(), owner, env, diagnosticRollbackCommand{}); err == nil {
		t.Fatal("injected transaction failure missing")
	}
	if h.count("tasks") != before || strings.Contains(out.String(), "app state transition") || strings.Contains(out.String(), privateText) {
		t.Fatal("rollback logged a committed transition or leaked error input", out.String())
	}
}

// Exercise failure after a real CreateTask has performed its writes, rather
// than seeding a terminal row or pretending an event was committed.
type diagnosticRollbackCommand struct{ CreateTask }

func (diagnosticRollbackCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if _, err := (CreateTask{}).Apply(ctx, x, tx); err != nil {
		return Outcome{}, err
	}
	return Outcome{}, errors.New("private-user-payload-and-reason")
}
