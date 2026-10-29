package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
)

// nativeIdentityCLI is the existing Orca CLI boundary; no model process runs.
type nativeIdentityCLI struct {
	mu              sync.Mutex
	prompt          string
	started         int64
	launched, sends int
	exists          bool
	transcript      string
	turn            int
}

// The fixture records the configured Codex session's real managed input and
// interruption boundaries, rather than presenting aggregate state as proof.
func (n *nativeIdentityCLI) record(kind string, payload any) error {
	row, err := json.Marshal(map[string]any{"type": kind, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "payload": payload})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(n.transcript, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(row, '\n'))
	return err
}

func (n *nativeIdentityCLI) startTurn() error {
	n.turn++
	if err := n.record("event_msg", map[string]any{"type": "task_started", "turn_id": fmt.Sprintf("turn-%d", n.turn)}); err != nil {
		return err
	}
	return n.record("response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": n.prompt}}})
}

func (n *nativeIdentityCLI) exec(_ context.Context, _ string, args []string) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	arg := func(name string) string {
		i := slices.Index(args, name)
		if i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	var result any
	switch args[0] + " " + args[1] {
	case "agent launch":
		n.launched++
		n.exists = true
		n.prompt = arg("--prompt")
		n.started = time.Now().UnixMilli()
		if err := n.record("session_meta", map[string]any{"id": "actual-provider-session"}); err != nil {
			return nil, err
		}
		if err := n.startTurn(); err != nil {
			return nil, err
		}
		result = map[string]any{"outcome": map[string]any{"kind": "terminal", "handle": "actual-terminal", "paneKey": "actual-tab:actual-pane"}, "worktreeId": "actual-worktree", "prompt": map[string]any{"outcome": "handed-to-terminal"}}
	case "worktree show":
		result = map[string]any{"worktree": map[string]any{"id": "actual-worktree"}}
	case "worktree ps":
		var agents []orca.Agent
		if n.exists {
			agents = append(agents, orca.Agent{PaneKey: "actual-tab:actual-pane", AgentType: "codex", State: "working", Prompt: n.prompt, StateStartedAt: n.started, ProviderSession: &orca.ProviderSession{ID: "actual-provider-session", TranscriptPath: n.transcript}})
		}
		result = map[string]any{"worktrees": []any{map[string]any{"worktreeId": "actual-worktree", "agents": agents}}}
	case "terminal list":
		var terms []orca.Terminal
		if n.exists {
			terms = append(terms, orca.Terminal{Handle: "actual-terminal", WorktreeID: "actual-worktree", TabID: "actual-tab", LeafID: "actual-pane", Connected: true})
		}
		result = map[string]any{"terminals": terms}
	case "terminal wait":
		result = map[string]any{"wait": map[string]any{"satisfied": false, "status": "busy"}}
	case "terminal send":
		n.sends++
		if arg("--text") == "\x1b" {
			if err := n.record("event_msg", map[string]any{"type": "turn_aborted", "turn_id": fmt.Sprintf("turn-%d", n.turn)}); err != nil {
				return nil, err
			}
		} else {
			n.prompt = arg("--text")
			n.started = time.Now().UnixMilli()
			if err := n.startTurn(); err != nil {
				return nil, err
			}
		}
		result = map[string]any{"send": map[string]any{"handle": "actual-terminal"}}
	case "terminal close":
		n.exists = false
		result = map[string]any{"close": map[string]any{"handle": "actual-terminal", "ptyKilled": true}}
	default:
		return nil, fmt.Errorf("unexpected native CLI command: %v", args)
	}
	return json.Marshal(map[string]any{"ok": true, "_meta": map[string]any{"runtimeId": "actual-orca-runtime"}, "result": result})
}
func (n *nativeIdentityCLI) counts() (int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.launched, n.sends
}

type nativeIdentityWorkspaces struct{ workspaces }

func (w *nativeIdentityWorkspaces) Prepare(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	p, err := w.workspaces.Prepare(ctx, as)
	if err != nil {
		return p, err
	}
	if err := os.MkdirAll(p.Dir, 0700); err != nil {
		return p, err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", p.Dir, "init", "-q").CombinedOutput(); err != nil {
		return p, fmt.Errorf("fixture git init: %w: %s", err, out)
	}
	p.ProviderID = "actual-worktree"
	p.Report.ProviderID = p.ProviderID
	p.Report.ProviderInstanceID = "actual-orca-runtime"
	p.Report.Path = p.Dir
	return p, nil
}
func TestNativeIdentityOrcaJournalRestartAndOwnerCLI(t *testing.T) {
	srv := newServer(t, time.Minute)
	srv.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}}, Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Repos: []string{"tend"}, Slots: 1, Worker: protocol.WorkerOrca, TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:tend-runner"}}}}, app.SyncConfig{})
	task := srv.createTask()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		_ = srv.store.Read(context.Background(), func(tx *store.ReadTx) error {
			rows, err := tx.Q().QueryContext(context.Background(), `SELECT run_id,seq,kind,body_json FROM run_logs ORDER BY run_id,seq`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var run, kind, body string
				var seq int64
				if err := rows.Scan(&run, &seq, &kind, &body); err != nil {
					return err
				}
				t.Log(run, seq, kind, body)
			}
			return rows.Err()
		})
	})

	n := newNode(t, srv, passing)
	native := &nativeIdentityCLI{transcript: filepath.Join(t.TempDir(), "native.jsonl")}
	sockets, err := os.MkdirTemp("", "p6i")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	ws := &nativeIdentityWorkspaces{workspaces{dir: t.TempDir()}}
	workers := orca.NewWorkers(orca.WorkerOptions{Client: &orca.Client{Exec: native.exec}, Agent: protocol.AgentCodex, BridgeCommand: "tend-runner", SocketDir: sockets, IDs: protocol.NewULIDGen(srv.now, nil), Now: srv.now, Poll: time.Millisecond, WaitChunk: time.Millisecond, Settle: time.Second})
	// Lose an accepted event response. The original journal sequence is retried.
	n.net.loseNext(protocol.OpRunnerEvents, 1)
	var activeJournal *journal.Journal
	var cancel context.CancelFunc
	var stopped chan error
	start := func() {
		j := n.journal()
		activeJournal = j
		ctx, stop := context.WithCancel(t.Context())
		cancel = stop
		stopped = make(chan error, 1)
		r := runner.New(runner.Config{ServerURL: srv.url, HostID: "linux-1", HostToken: hostToken, HTTPClient: &http.Client{Transport: n.net}, Journal: j, Worker: protocol.WorkerOrca, Workers: workers, Workspaces: ws, Now: srv.now, IDs: protocol.NewULIDGen(srv.now, nil), Log: runnerTestLog(t), RetryDelay: 10 * time.Millisecond, EventFlush: 10 * time.Millisecond})
		go func() { err := r.Run(ctx); _ = j.Close(); stopped <- err }()
	}
	stop := func() {
		if cancel == nil {
			return
		}
		cancel()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			t.Fatal("runner did not stop")
		}
		cancel = nil
	}
	defer stop()
	start()
	var runID string
	eventually(t, "native session fact reaches original run", func() bool {
		if srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=?`, task) == 0 {
			return false
		}
		if err := srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT run_id FROM runs WHERE task_id=? ORDER BY created_at DESC LIMIT 1`, task).Scan(&runID)
		}); err != nil {
			t.Fatal(err)
		}
		v, err := srv.app.NativeEntry(t.Context(), owner, runID, "")
		return err == nil && v.SessionRecorded
	})
	original, err := srv.app.NativeEntry(t.Context(), owner, runID, "")
	if err != nil || original.Identity == nil || original.Identity.Terminal != "actual-terminal" || original.Identity.SessionID != "actual-provider-session" || original.Identity.RuntimeID != "actual-orca-runtime" {
		t.Fatal(original, err)
	}
	cli := newOwnerCLI(t, filepath.Join(t.TempDir(), "config"))
	cli.run("", "login", "--server", srv.startAPI())
	launched, sends := native.counts()
	requests := srv.count(`SELECT COUNT(*) FROM request_records`)
	outbox := srv.count(`SELECT COUNT(*) FROM outbox`)
	var read protocol.NativeEntry
	if err := json.Unmarshal([]byte(cli.run("", "run", "native-entry", runID)), &read); err != nil || read.Identity == nil || *read.Identity != *original.Identity {
		t.Fatal(read, err)
	}
	if afterLaunch, afterSend := native.counts(); afterLaunch != launched || afterSend != sends || srv.count(`SELECT COUNT(*) FROM request_records`) != requests || srv.count(`SELECT COUNT(*) FROM outbox`) != outbox {
		t.Fatal("owner navigation sent input/control or launched a session")
	}
	stop() // closes the real local journal before opening a new runner instance.
	start()
	eventually(t, "same run recovers original native identity after journal reopen", func() bool {
		record, err := activeJournal.Run(t.Context(), runID)
		if err != nil || record.Attempts < 2 {
			return false
		}
		v, err := srv.app.NativeEntry(t.Context(), owner, runID, "")
		return err == nil && v.IdentitySequence > original.IdentitySequence && v.SessionRecorded && v.Identity != nil && v.Identity.SessionID == original.Identity.SessionID && v.Identity.Terminal == original.Identity.Terminal
	})
	if launches, _ := native.counts(); launches != 1 || srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=? AND role='implement'`, task) != 1 {
		t.Fatal("restart replaced original native session or run", launches)
	}
}
