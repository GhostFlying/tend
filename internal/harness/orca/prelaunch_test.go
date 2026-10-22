package orca

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestOwnerConfigPrelaunchProofRequiresFreshFirstAttempt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		attempt int
		thread  string
		proof   bool
	}{
		{"fresh first", 1, "", true},
		{"unknown attempt", 0, "", false},
		{"later empty attempt", 2, "", false},
		{"existing terminal", 1, `{"worktree":"folder:old","terminal":"term_old","pane":"tab:1"}`, false},
		{"restored terminal", 2, `{"worktree":"folder:old","terminal":"term_old","pane":"tab:1"}`, false},
		{"damaged old state", 1, `{"worktree":`, false},
		{"unknown old native identity", 1, "native-thread", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := filepath.Join(dir, ".codex", "config.toml")
			if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
				t.Fatal(err)
			}
			owner := []byte("# owner config: never replace\n")
			if err := os.WriteFile(cfg, owner, 0o600); err != nil {
				t.Fatal(err)
			}
			clock := testkit.NewClock()
			sockets, err := os.MkdirTemp("", "tnp-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(sockets) })
			calls := 0
			c := &Client{Exec: func(context.Context, string, []string) ([]byte, error) {
				calls++
				return nil, errors.New("unexpected provider call")
			}}
			ws := NewWorkers(WorkerOptions{Client: c, Agent: protocol.AgentCodex, IDs: testkit.NewIDGen(clock, 1), Now: clock.Now, SocketDir: sockets})
			worker, err := ws.Start(t.Context(), harness.WorkerSpec{RunID: "prelaunch", Dir: dir, ProviderID: "folder:owned", StateDir: filepath.Join(dir, "private"), Attempt: tc.attempt, Thread: tc.thread})
			if worker != nil || err == nil || errors.Is(err, harness.ErrWorkerNotStarted) != tc.proof {
				t.Fatalf("proof=%v worker=%v err=%v", tc.proof, worker, err)
			}
			if !strings.Contains(err.Error(), "already exists and is not tend's") {
				t.Fatalf("did not exercise owner configuration conflict: %v", err)
			}
			if calls != 0 {
				t.Fatalf("provider called before config failure: %d", calls)
			}
			if b, err := os.ReadFile(cfg); err != nil || string(b) != string(owner) {
				t.Fatalf("owner config changed: %q %v", b, err)
			}
		})
	}
}

func TestUnknownLaunchNeverClaimsNoWorkerStarted(t *testing.T) {
	f := newWorkerFixture(t)
	transportErr := errors.New("provider accepted launch but reply was lost")
	original := f.ws.opts.Client.Exec
	f.ws.opts.Client.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		b, err := original(ctx, dir, args)
		if args[0] == "agent" && args[1] == "launch" {
			return nil, transportErr
		}
		return b, err
	}
	spec := f.spec(protocol.RoleImplement)
	spec.Attempt = 1
	spec.ProviderID = f.orca.wt
	worker, err := f.ws.Start(t.Context(), spec)
	if worker != nil || !errors.Is(err, transportErr) || errors.Is(err, harness.ErrWorkerNotStarted) {
		t.Fatalf("uncertain launch certified absent: worker=%v err=%v", worker, err)
	}
	if f.orca.count("agent launch", nil) != 1 || f.state().Marker == "" || f.state().Worktree == "" {
		t.Fatalf("unknown launch identity lost: %+v", f.state())
	}
	f.orca.mu.Lock()
	defer f.orca.mu.Unlock()
	if len(f.orca.terms) != 1 {
		t.Fatalf("test did not exercise an accepted unknown launch: %d", len(f.orca.terms))
	}
}

func TestBeginJournalFailureIsNotPrelaunchCertified(t *testing.T) {
	f := newWorkerFixture(t)
	spec := f.spec(protocol.RoleImplement)
	spec.Attempt = 1
	spec.ProviderID = f.orca.wt
	writeErr := errors.New("journal unavailable")
	spec.OnThread = func(string) error { return writeErr }
	worker, err := f.ws.Start(t.Context(), spec)
	if worker != nil || !errors.Is(err, writeErr) || errors.Is(err, harness.ErrWorkerNotStarted) {
		t.Fatalf("begin error received preflight proof: %v", err)
	}
	if f.orca.count("agent launch", nil) != 0 {
		t.Fatal("launched after failed write-ahead state")
	}
}
