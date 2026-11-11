package mainthread

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRunnerEventDiagnosticsKeepHistoricalRunAndSequence(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	seedClosingReceiptMetadata(t, e, "completed", true)
	const first = "receipt-reader-run"
	const second = "second-original-run"
	const reader = "current-reader-run"
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for n, id := range []string{second, reader} {
			if err := tx.InsertRun(t.Context(), store.RunRow{RunID: id, TaskID: "task_cached", ActionID: "action_cached", Generation: int64(n + 2), Role: protocol.RoleIssueManager, DesiredState: protocol.DesiredRun, ObservedState: protocol.RunUnknown}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `UPDATE runs SET native_thread_id=(SELECT native_thread_id FROM runs WHERE run_id=?),native_thread_json=(SELECT native_thread_json FROM runs WHERE run_id=?) WHERE run_id=?`, first, first, reader); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES('diagnostic_close',?,'close','{}','pending',0)`, reader); err != nil {
			return err
		}
		for _, id := range []string{first, second} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES(?,14,'{"thread_id":"original-thread","native":{"Kind":"item_started","Text":"private event content"}}')`, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var observed []store.WriteObservation
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) { observed = append(observed, o) })(e.store)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	session := coordination.New(ctx, &runnerTransport{app: e.app, run: reader, origins: map[int64]managerEventOrigin{}})
	defer func() { cancel(); <-session.Done() }()
	thread, err := session.ResumeThread(ctx, "original-thread", harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	d := e.m.sessionDiagnostics(&hostedconfig.Selection{RunnerRunID: reader})
	for n, origin := range []string{first, second} {
		var ev harness.Event
		select {
		case ev = <-thread.Events():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if ev.CoordinatorSequence != int64(n+1) || ev.OriginRunID != origin || ev.OriginSequence != 14 {
			t.Fatalf("historical identity remapped: %+v", ev)
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), origin) || strings.Contains(string(raw), "OriginSequence") || strings.Contains(string(raw), "CoordinatorSequence") {
			t.Fatalf("local diagnostic metadata leaked onto wire: %s", raw)
		}
		eventCtx := d.eventContext(ctx, ev)
		if err := e.store.Write(eventCtx, func(*store.Tx) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := thread.(harness.EventAcknowledger).AcknowledgeEvent(eventCtx, ev); err != nil {
			t.Fatal(err)
		}
		for _, o := range observed[len(observed)-2:] {
			if o.Source.RunID != origin || o.Source.EventSequence != 14 || o.Source.AttachmentRunID != reader || o.Source.CoordinatorSequence != int64(n+1) {
				t.Fatalf("apply/ACK attributed to synthetic identity: %+v", o)
			}
		}
	}
	if got := e.count(`SELECT COUNT(*) FROM manager_native_events WHERE applied=1`); got != 2 {
		t.Fatalf("original ACK behavior changed: %d", got)
	}
	// Transports without local origin metadata remain explicitly unknown; never
	// manufacture an original sequence from the attachment's synthetic cursor.
	unknown := d.eventContext(ctx, harness.Event{CoordinatorSequence: 99})
	if err := e.store.Write(unknown, func(*store.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	last := observed[len(observed)-1]
	if last.Source.RunID != "" || last.Source.EventSequence != 0 || last.Source.AttachmentRunID != reader || last.Source.CoordinatorSequence != 99 {
		t.Fatalf("unknown origin guessed: %+v", last)
	}
}
