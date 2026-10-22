//go:build integration

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

// TestIntegration_HostedMainAgentReads runs the hosted main agent as
// tend-server does, in the traex home of the user running the test (D68),
// and asks it about a file outside its directory: the sandbox lets it read
// the file and the turn does not stop at an approval.
func TestIntegration_HostedMainAgentReads(t *testing.T) {
	srv := newServer(t, time.Minute)
	dir := t.TempDir()
	const word = "periwinkle-4127"
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("The code word is "+word+".\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	cb := contextbuilder.New(srv.app, srv.store, srv.ids, contextbuilder.Config{})
	front := mainthread.New(srv.app, cb, tools.New(srv.app, srv.store, cb, quiet()), mainthread.Config{
		Instance: "main",
		Thread:   mainthread.ThreadConfig(t.TempDir(), traexModel()),
		Open: func(ctx context.Context) (harness.Session, error) {
			return traex.Start(ctx, traex.Options{Version: "it", Env: []string{protocol.AddonEnv + "=off"}})
		},
		Log: quiet(),
	})
	d := dispatch.New(srv.app, map[string]dispatch.Handler{app.MainThreadDestination: front}, srv.signal, time.Now, dispatch.Config{}, quiet())
	wg.Go(func() { _ = front.Run(ctx) })
	wg.Go(func() { _ = d.Run(ctx) })

	srv.say("This is not about a task. Read the file " + filepath.Join(dir, "notes.txt") + " and tell me the code word in it.")
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if srv.count(`SELECT COUNT(*) FROM conversation_messages WHERE author = 'main_agent' AND body LIKE ?`, "%"+word+"%") > 0 {
			return
		}
		if srv.count(`SELECT COUNT(*) FROM main_turns WHERE status IN ('completed', 'failed', 'interrupted')`) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	var replies []string
	_ = srv.store.Read(context.Background(), func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(context.Background(), `SELECT body FROM conversation_messages WHERE author = 'main_agent'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b string
			_ = rows.Scan(&b)
			replies = append(replies, b)
		}
		return rows.Err()
	})
	t.Fatalf("the main agent did not report the code word; replies: %q", replies)
}
