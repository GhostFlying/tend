package journal

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func largeSpoolBody() json.RawMessage {
	return json.RawMessage(`{"thread_id":"native-thread","text":"` + strings.Repeat("汉字🙂", 3000) + `"}`)
}
func spoolFiles(t *testing.T, j *Journal) []string {
	t.Helper()
	var files []string
	_ = filepath.WalkDir(j.path+".spool", func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func TestSpoolDeduplicatesEventAndUnknownRequestAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	ctx := t.Context()
	as := protocol.Assignment{RunID: "run_spool"}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	body := largeSpoolBody()
	for range 2 {
		if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{Kind: "native.turn_completed", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	req := Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "unknown-original", Operation: protocol.OpRunsSubmitArtifact, Payload: body}}
	if err := j.PutRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := j.db.QueryRowContext(ctx, `SELECT body_json FROM events LIMIT 1`).Scan(&stored); err != nil || len(stored) > 512 {
		t.Fatal("journal retained large event body", len(stored), err)
	}
	if files := spoolFiles(t, j); len(files) != 1 {
		t.Fatal("same body was copied", files)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	events, err := j.PendingEvents(ctx, as.RunID, 100)
	if err != nil || len(events) != 2 || string(events[1].Body) != string(body) {
		t.Fatal("offline event read changed", err)
	}
	terminal, ok, err := j.LatestNativeTerminal(ctx, as.RunID, "native-thread")
	if err != nil || !ok || string(terminal.Body) != string(body) {
		t.Fatal("terminal lookup lost body", ok, err)
	}
	if err = j.AckEvents(ctx, as.RunID, 2); err != nil {
		t.Fatal(err)
	}
	if n, err := j.PruneSpool(ctx); err != nil || n != 0 {
		t.Fatal("unconfirmed request body was collected", n, err)
	}
	pending, err := j.Requests(ctx)
	if err != nil || len(pending) != 1 || string(pending[0].Envelope.Payload) != string(body) {
		t.Fatal("unknown request changed", pending, err)
	}
	if err = j.DeleteRequest(ctx, req.Envelope.RequestID); err != nil {
		t.Fatal(err)
	}
	if n, err := j.PruneSpool(ctx); err != nil || n != int64(len(body)) {
		t.Fatal("confirmed transmission body not collected", n, err)
	}
}

func TestSpoolMissingCorruptionReservedLiteralAndFrozenReference(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "reserved", "frozen"} {
		t.Run(kind, func(t *testing.T) {
			j := open(t, filepath.Join(t.TempDir(), "journal.db"))
			defer j.Close()
			ctx := t.Context()
			as := protocol.Assignment{RunID: "run"}
			if err := j.AcceptAssignment(ctx, as, ""); err != nil {
				t.Fatal(err)
			}
			body := largeSpoolBody()
			if kind == "reserved" {
				body = json.RawMessage(`{"tend_spool":{"version":1,"sha256":"literal","bytes":123}}`)
			}
			if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{Kind: "output", Body: body}); err != nil {
				t.Fatal(err)
			}
			files := spoolFiles(t, j)
			if len(files) != 1 {
				t.Fatal(files)
			}
			switch kind {
			case "missing":
				if err := os.Remove(files[0]); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(files[0], []byte("damaged"), 0600); err != nil {
					t.Fatal(err)
				}
			case "frozen":
				var marker string
				if err := j.db.QueryRowContext(ctx, `SELECT body_json FROM events`).Scan(&marker); err != nil {
					t.Fatal(err)
				}
				if _, err := j.db.ExecContext(ctx, `INSERT INTO retained_run_resumes(run_id,authority_json) VALUES(?,json_object('stop_history',?))`, as.RunID, marker); err != nil {
					t.Fatal(err)
				}
				if err := j.AckEvents(ctx, as.RunID, 1); err != nil {
					t.Fatal(err)
				}
				if n, err := j.PruneSpool(ctx); err != nil || n != 0 {
					t.Fatal("frozen original collected", n, err)
				}
				return
			}
			events, err := j.PendingEvents(ctx, as.RunID, 10)
			if kind == "reserved" {
				if err != nil || len(events) != 1 || string(events[0].Body) != string(body) {
					t.Fatal("literal reserved JSON not preserved", err)
				}
			} else if err == nil {
				t.Fatal("missing/corrupt original silently accepted")
			}
		})
	}
}

func TestSpoolMigrationPreservesInlineAndConcurrentCollection(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.db"))
	defer j.Close()
	ctx := t.Context()
	as := protocol.Assignment{RunID: "run"}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	body := largeSpoolBody()
	if _, err := j.db.ExecContext(ctx, `INSERT INTO events(run_id,seq,kind,body_json) VALUES(?,1,'legacy',?)`, as.RunID, string(body)); err != nil {
		t.Fatal(err)
	}
	if n, err := j.MigrateSpool(ctx, 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := j.MigrateSpool(ctx, 1); err != nil || n != 0 {
		t.Fatal("repeat migration changed body", n, err)
	}
	if _, err := j.db.ExecContext(ctx, `UPDATE runs SET next_seq=2 WHERE run_id=?`, as.RunID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				if worker == 0 {
					if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{Kind: "output", Body: body}); err != nil {
						t.Error(err)
						return
					}
				} else {
					if _, err := j.PruneSpool(ctx); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	events, err := j.PendingEvents(ctx, as.RunID, 100)
	if err != nil || len(events) != 9 {
		t.Fatal(len(events), err)
	}
	for _, event := range events {
		if fmt.Sprintf("%x", sha256.Sum256(event.Body)) != fmt.Sprintf("%x", sha256.Sum256(body)) {
			t.Fatal("body changed")
		}
	}
}

func TestNativeReadOnlyHasNoJournalHistoryAndSuccessfulCommandSettles(t *testing.T) {
	j := open(t, filepath.Join(t.TempDir(), "journal.db"))
	defer j.Close()
	ctx := t.Context()
	as := protocol.Assignment{RunID: "manager"}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"models", "find", "usage"} {
		if _, _, err := j.ManagerOperation(ctx, as.RunID, protocol.ManagerNativeRequest{ID: method, Method: method, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM manager_native_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read-only native RPC was journaled", count, err)
	}
	command := protocol.ManagerNativeRequest{ID: "turn", Method: "turn", Payload: largeSpoolBody()}
	if _, _, err := j.ManagerOperation(ctx, as.RunID, command); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteManagerOperation(ctx, protocol.ManagerNativeReply{ID: command.ID, Result: largeSpoolBody()}); err != nil {
		t.Fatal(err)
	}
	reply, _ := json.Marshal(protocol.ManagerNativeReply{ID: command.ID, Result: largeSpoolBody()})
	if err := j.PutRequest(ctx, Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "receipt", Operation: protocol.OpManagerNativeReply, Payload: reply}}); err != nil {
		t.Fatal(err)
	}
	if err := j.DeleteRequest(ctx, "receipt"); err != nil {
		t.Fatal(err)
	}
	if err := j.db.QueryRowContext(ctx, `SELECT count(*) FROM manager_native_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("completed RPC retained its body", count, err)
	}
}

func TestSpoolWorkerResultRequiresExactSuccessfulFinalAcknowledgement(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			j := open(t, filepath.Join(t.TempDir(), "journal.db"))
			defer j.Close()
			ctx := t.Context()
			as := protocol.Assignment{RunID: "run_result"}
			if err := j.AcceptAssignment(ctx, as, ""); err != nil {
				t.Fatal(err)
			}
			body := largeSpoolBody()
			if err := j.WorkerDone(ctx, as.RunID, body); err != nil {
				t.Fatal(err)
			}
			req := Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-final", Operation: protocol.OpRunsSubmitArtifact, Payload: body}}
			if err := j.Finish(ctx, as.RunID, PhaseSubmitting, req); err != nil {
				t.Fatal(err)
			}
			if err := j.AcknowledgeResult(ctx, as.RunID, req.Envelope.RequestID); err == nil {
				t.Fatal("live executor result became reclaimable")
			}
			if err := j.ConfirmStopped(ctx, as.RunID); err != nil {
				t.Fatal(err)
			}
			if err := j.AcknowledgeResult(ctx, as.RunID, "unrelated"); err == nil {
				t.Fatal("unrelated ACK authorized collection")
			}
			if ack {
				if err := j.AcknowledgeResult(ctx, as.RunID, req.Envelope.RequestID); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.Done(ctx, as.RunID); err != nil {
				t.Fatal(err)
			}
			bytes, err := j.PruneSpool(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if ack && bytes != int64(len(body)) || !ack && bytes != 0 {
				t.Fatal("result collection ignored true ACK", ack, bytes)
			}
		})
	}
}
