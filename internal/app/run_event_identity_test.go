package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRunEventIdentitySurvivesRepeatedBatchAndRestart(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "immutable run facts", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	text, _ := json.Marshal(strings.Repeat("原始输出🙂", 12000))
	body := json.RawMessage(`{"at":1700,"optional":null,"parts":[],"count":9007199254740993,"text":` + string(text) + `}`)
	reordered := json.RawMessage(` {"text":` + string(text) + `,"parts":[],"count":9007199254740993,"optional":null,"at":1700} `)
	first := protocol.RunEvent{Seq: 1, NativeID: "original-item", Kind: "item.completed", Body: body}
	repeated := first
	repeated.Body = reordered
	p := runPrincipal(as.RunID, as.Role)
	result, err := h.app.RecordRunEvents(t.Context(), p, as.RunID, []protocol.RunEvent{first, repeated})
	if err != nil || result.Accepted != 1 || h.count("run_logs") != 1 {
		t.Fatalf("same-batch replay: result=%+v err=%v logs=%d", result, err, h.count("run_logs"))
	}
	var recorded string
	var recordedAt int64
	readOriginal := func() {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT body_json,created_at FROM run_logs WHERE run_id=? AND seq=1`, as.RunID).Scan(&recorded, &recordedAt)
		}); err != nil {
			t.Fatal(err)
		}
	}
	readOriginal()
	originalReference, originalAt := recorded, recordedAt
	reopenArchiveCombinationHarness(t, h)
	result, err = h.app.RecordRunEvents(t.Context(), p, as.RunID, []protocol.RunEvent{repeated})
	if err != nil || result.Accepted != 0 || h.count("run_logs") != 1 {
		t.Fatalf("replay after actual store reopen: result=%+v err=%v", result, err)
	}
	readOriginal()
	if recorded != originalReference || recordedAt != originalAt {
		t.Fatal("a retry rewrote the immutable stored event")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, exists, err := store.RunLogAt(t.Context(), tx.Q(), as.RunID, 1)
		if err != nil {
			return err
		}
		if !exists || row.NativeID != first.NativeID || row.Kind != first.Kind || !store.SemanticJSONEqual(row.BodyJSON, string(first.Body)) {
			t.Fatal("reopened event lost its actual identity or original body", exists, row.Kind, row.NativeID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunEventConflictsRejectChangesAndRollBackLaterBatch(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "reject changed facts", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	first := protocol.RunEvent{Seq: 1, NativeID: "original-item", Kind: "item.completed", Body: json.RawMessage(`{"at":1700,"optional":null,"parts":[],"text":"original","count":9007199254740993}`)}
	p := runPrincipal(as.RunID, as.Role)
	if result, err := h.app.RecordRunEvents(t.Context(), p, as.RunID, []protocol.RunEvent{first}); err != nil || result.Accepted != 1 {
		t.Fatalf("initial event: %+v %v", result, err)
	}
	beforeEvents := h.count("events")
	for _, tc := range []struct {
		name   string
		change func(*protocol.RunEvent)
	}{
		{"kind", func(e *protocol.RunEvent) { e.Kind = "item.started" }},
		{"native_identity", func(e *protocol.RunEvent) { e.NativeID = "other-item" }},
		{"timestamp", func(e *protocol.RunEvent) {
			e.Body = json.RawMessage(strings.Replace(string(e.Body), `"at":1700`, `"at":1701`, 1))
		}},
		{"missing_field", func(e *protocol.RunEvent) {
			e.Body = json.RawMessage(strings.Replace(string(e.Body), `"optional":null,`, "", 1))
		}},
		{"null_to_array", func(e *protocol.RunEvent) {
			e.Body = json.RawMessage(strings.Replace(string(e.Body), `"optional":null`, `"optional":[]`, 1))
		}},
		{"array_to_null", func(e *protocol.RunEvent) {
			e.Body = json.RawMessage(strings.Replace(string(e.Body), `"parts":[]`, `"parts":null`, 1))
		}},
		{"string", func(e *protocol.RunEvent) {
			e.Body = json.RawMessage(strings.Replace(string(e.Body), `"original"`, `"changed"`, 1))
		}},
		{"large_integer", func(e *protocol.RunEvent) {
			e.Body = json.RawMessage(strings.Replace(string(e.Body), `9007199254740993`, `9007199254740992`, 1))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conflict := first
			tc.change(&conflict)
			result, err := h.app.RecordRunEvents(t.Context(), p, as.RunID, []protocol.RunEvent{
				{Seq: 2, Kind: protocol.EventRunWaiting},
				conflict,
				{Seq: 3, Kind: "item.started", NativeID: "later-item"},
			})
			if protocol.CodeOf(err) != protocol.CodeConflictPayload || result.Accepted != 0 {
				t.Fatalf("changed event accepted: result=%+v err=%v", result, err)
			}
			if h.count("run_logs") != 1 || h.count("events") != beforeEvents || h.run(as.RunID).ObservedState != protocol.RunRunning {
				t.Fatal("a rejected batch persisted a prefix, suffix or state transition")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				row, exists, err := store.RunLogAt(t.Context(), tx.Q(), as.RunID, 1)
				if err == nil && (!exists || row.Kind != first.Kind || row.NativeID != first.NativeID || !store.SemanticJSONEqual(row.BodyJSON, string(first.Body))) {
					t.Fatal("conflicting retry changed the original event", row)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunEventConflictingDuplicateRollsBackInitialBatch(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "atomic first upload", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	beforeEvents := h.count("events")
	result, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{
		{Seq: 1, Kind: protocol.EventRunWaiting, Body: json.RawMessage(`{"at":1700}`)},
		{Seq: 1, Kind: protocol.EventRunWaiting, Body: json.RawMessage(`{"at":1701}`)},
	})
	if protocol.CodeOf(err) != protocol.CodeConflictPayload || result.Accepted != 0 {
		t.Fatalf("conflicting first batch accepted: %+v %v", result, err)
	}
	if h.count("run_logs") != 0 || h.count("events") != beforeEvents || h.run(as.RunID).ObservedState != protocol.RunRunning {
		t.Fatal("initial conflicting duplicate committed part of its batch")
	}
}
