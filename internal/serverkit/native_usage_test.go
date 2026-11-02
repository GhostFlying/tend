package serverkit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
)

func TestStoppedHostedBacklogDrainsWithoutAnAgentSession(t *testing.T) {
	clock := testkit.NewClock()
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := app.New(s, testkit.NewIDGen(clock, 41), clock.Now)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	var body strings.Builder
	body.WriteString("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread\",\"session_id\":\"thread\",\"cli_version\":\"test\"}}\n")
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&body, `{"type":"token_usage_record","ordinal":%d,"timestamp":%q,"payload":{"thread_id":"thread","session_id":"thread","turn_id":"turn-%d","response_id":"response-%d","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`+"\n", i, clock.Now().Add(-time.Second).Format(time.RFC3339Nano), i, i)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0600); err != nil {
		t.Fatal(err)
	}
	source, _ := json.Marshal(nativeusage.Source{Path: path, Provider: "traex", ThreadID: "thread", SessionID: "thread"})
	if err := a.RegisterLocalUsage(t.Context(), app.LocalUsageSource{ID: "stopped-main", Source: source, WriterStopped: true}); err != nil {
		t.Fatal(err)
	}
	collector := centralUsage{app: a, now: clock.Now, log: slog.New(slog.DiscardHandler), discovery: nativeusage.NewDiscovery()}
	for range 2 {
		if err := collector.sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "conversation:main", 0, clock.Now())
		if err != nil {
			return err
		}
		if view.Totals.Total != 3300 || view.Totals.Requests != 300 {
			t.Fatal(view.Totals)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sources, err := a.LocalUsageSources(t.Context(), "main")
	if err != nil || len(sources) != 1 || sources[0].Coverage != "caught_up" {
		t.Fatal(sources, err)
	}
}
