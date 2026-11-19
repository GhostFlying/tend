package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestManagerHistoryReadNeedsNeitherRunSlotNorModel(t *testing.T) {
	for _, retired := range []bool{false, true} {
		name := "current"
		if retired {
			name = "retired"
		}
		t.Run(name, func(t *testing.T) { testManagerHistoryRead(t, retired) })
	}
}

func testManagerHistoryRead(t *testing.T, retired bool) {
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	pin := protocol.RunExecutionPin{Purpose: "issue_manager", Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "owner"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "codex", Revision: 1}, InventoryRevision: 1, SnapshotID: "original", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "unavailable-original-model"}, Worker: protocol.WorkerAppServer}
	native := &receiptBridgeSession{done: make(chan struct{}), thread: &receiptBridgeThread{events: make(chan harness.Event)}}
	resolver := &receiptBridgeResolver{native: native, pin: pin}
	r := New(Config{HostID: "host", Journal: j, Executions: resolver, Now: time.Now, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
	read := protocol.ManagerHistoryRead{ID: "original-observation", RunID: "original-run", ConversationID: "original-conversation", Execution: pin, ThreadID: "original-thread", MessageID: "original-repair", TurnID: "original-turn"}
	if retired {
		r.cfg.JournalRoot, r.cfg.JournalID = t.TempDir(), "accepting"
		original := ""
		read.RetiredJournal = &original
		key := sha256.Sum256([]byte(read.ConversationID))
		if err := os.MkdirAll(filepath.Join(r.cfg.JournalRoot, "manager-sessions", hex.EncodeToString(key[:])), 0700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := os.Stat(filepath.Join(j.Directory(), "manager-sessions")); !os.IsNotExist(err) {
				t.Fatal("retired history touched the accepting journal", err)
			}
		})
	}
	r.acceptHistoryReads(t.Context(), []protocol.ManagerHistoryRead{read}, nil)
	r.wg.Wait()
	replies := r.historyReplies()
	if len(replies) != 1 || replies[0].Error != "" {
		t.Fatal(replies)
	}
	var result coordination.FindResult
	if err := json.Unmarshal(replies[0].Result, &result); err != nil || !result.Found || result.Receipt.ID != "original-turn" {
		t.Fatal(result, err)
	}
	r.acceptHistoryReads(t.Context(), []protocol.ManagerHistoryRead{read}, nil)
	r.wg.Wait()
	if resolver.business.Load() != 0 || resolver.receipts.Load() != 1 || native.models != 0 || native.starts != 0 || native.resumes != 0 || native.histories != 1 || native.thread.reads != 1 {
		t.Fatal("history replay invoked business or repeated an unacknowledged read", native)
	}
	if runs, err := j.Runs(t.Context()); err != nil || len(runs) != 0 {
		t.Fatal("history reader created an execution journal", runs, err)
	}
	r.acceptHistoryReads(t.Context(), nil, []string{read.ID})
	if len(r.historyReplies()) != 0 {
		t.Fatal("acknowledged observation retained its response")
	}
}
