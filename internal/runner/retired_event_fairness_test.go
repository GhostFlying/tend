package runner

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRetiredJournalFlushesOtherRunsAfterUploadTimeout(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	r.cfg.JournalID, r.cfg.JournalRoot = "accepting", t.TempDir()
	r.cfg.RequestTimeout = time.Second
	old, err := journal.Open(t.Context(), filepath.Join(r.cfg.JournalRoot, "journal.db"), r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-blocked", "b-healthy"} {
		as := protocol.Assignment{RunID: id, EventSync: true}
		if err := old.AcceptAssignment(t.Context(), as, ""); err != nil {
			t.Fatal(err)
		}
		if err := old.AppendEvent(t.Context(), id, protocol.RunEvent{Kind: "output", Body: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	var healthy, blocked atomic.Int64
	r.c.base = "http://test"
	r.c.http = &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			return nil, err
		}
		if env.Target.ID == "a-blocked" {
			blocked.Add(1)
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		healthy.Add(1)
		return finalEventsResponse(env, false), nil
	})}
	var c retiredCursor
	for range 2 {
		c, _ = r.serviceRetiredJournal(t.Context(), "", c, protocol.RetiredJournalWork{})
	}
	if blocked.Load() == 0 {
		t.Fatal("the first Run never reached its blocked upload")
	}
	if healthy.Load() != 1 {
		t.Fatal("healthy retired Run was starved by first Run's deadline on both passes")
	}
}
