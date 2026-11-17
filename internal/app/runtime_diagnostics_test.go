package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
