package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type fakeNative struct {
	readMode                      NativeReadMode
	thread                        NativeThread
	created, started, interrupted int
	reviewed                      int
	input, turn                   string
	busy, complete, loseReply     bool
}

func (n *fakeNative) NewThread(context.Context, protocol.MaintenanceBackend, string, string) (NativeThread, error) {
	n.created++
	return NativeThread{ID: fmt.Sprintf("native-%d", n.created), Path: "rollout.jsonl", Sandbox: map[string]any{"type": "workspaceWrite", "excludeSlashTmp": true}}, nil
}
func (n *fakeNative) Read(_ context.Context, _ protocol.MaintenanceBackend, _ string, mode NativeReadMode) (NativeState, error) {
	n.readMode = mode
	s := NativeState{Busy: n.busy}
	if n.turn != "" {
		status := "inProgress"
		if n.complete {
			status = "completed"
			if n.interrupted > 0 {
				status = "interrupted"
			}
		}
		s.Turns = []NativeTurn{{ID: n.turn, InputID: n.input, Status: status}}
	}
	return s, nil
}
func (n *fakeNative) Start(_ context.Context, _ protocol.MaintenanceBackend, thread NativeThread, input, _ string) (string, error) {
	n.thread = thread
	n.started++
	n.input = input
	n.turn = fmt.Sprintf("turn-%d", n.started)
	n.busy = true
	n.complete = false
	if n.loseReply {
		return "", errors.New("lost native receipt")
	}
	return n.turn, nil
}
func (n *fakeNative) Interrupt(context.Context, protocol.MaintenanceBackend, string, string) error {
	n.interrupted++
	return nil
}

func (n *fakeNative) Review(ctx context.Context, b protocol.MaintenanceBackend, thread NativeThread, instructions string) (string, error) {
	n.reviewed++
	_, tail, _ := strings.Cut(instructions, "request_id=")
	input := strings.TrimSuffix(strings.Fields(tail)[0], ".")
	return n.Start(ctx, b, thread, input, instructions)
}

func fixture(t *testing.T) (*Controller, *fakeNative, Config, Options) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateDir: dir, ControlSocket: filepath.Join(dir, "control.sock"), Workspace: dir, InstructionsFile: filepath.Join(dir, "instructions"), Backend: protocol.MaintenanceBackend{Agent: "codex", Socket: "/native.sock", Sandbox: "workspace-write", ApprovalPolicy: "never"}}
	if err := os.WriteFile(cfg.InstructionsFile, []byte("Maintain only the configured Tend workspace."), 0600); err != nil {
		t.Fatal(err)
	}
	n := &fakeNative{}
	i := 0
	opts := Options{Now: func() time.Time { return time.Unix(1000, 0) }, ID: func() string { i++; return fmt.Sprint(i) }, Native: n}
	opts.Head = func(context.Context, string) (string, error) { return strings.Repeat("a", 40), nil }
	c, err := Open(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c.lock != nil {
			_ = c.Close()
		}
	})
	return c, n, cfg, opts
}
func command(t *testing.T, c *Controller, action string, req protocol.MaintenanceRequest) {
	t.Helper()
	req.RequestID = c.opts.ID()
	req.ExpectedRevision = c.Status().Revision
	if _, err := c.Command(context.Background(), action, req); err != nil {
		t.Fatal(err)
	}
}
func step(t *testing.T, c *Controller) {
	t.Helper()
	if err := c.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func launch(t *testing.T, c *Controller) {
	t.Helper()
	command(t, c, "audit", protocol.MaintenanceRequest{})
	step(t, c)
	step(t, c)
}

func TestFault_MaintenanceRestartBeforeFirstInput(t *testing.T) {
	c, n, cfg, opts := fixture(t)
	command(t, c, "audit", protocol.MaintenanceRequest{})
	step(t, c)
	id := c.Status().Active.SessionID
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.lock = nil
	d, err := Open(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	step(t, d)
	if n.created != 1 || n.started != 1 || n.thread.ID != id || n.thread.Sandbox["excludeSlashTmp"] != true || n.readMode != ReadMetadata {
		t.Fatal("pre-input recovery lost native identity or resolved policy", n.thread, n.readMode)
	}
}

func TestFault_MaintenanceReviewContinuationSurvivesBackendSwitch(t *testing.T) {
	c, n, _, _ := fixture(t)
	launch(t, c)
	command(t, c, "review", protocol.MaintenanceRequest{Base: "main", Head: strings.Repeat("a", 40)})
	n.complete, n.busy = true, false
	step(t, c)
	step(t, c)
	if n.reviewed != 1 {
		t.Fatal("review did not start")
	}
	backend := c.Status().Backend
	backend.Agent = "traex"
	command(t, c, "config", protocol.MaintenanceRequest{Backend: &backend, Mode: "when-idle"})
	n.complete, n.busy = true, false
	step(t, c)
	if s := c.Status(); s.Review.Phase != "completed" || s.Active != nil || len(s.Pending) == 0 || s.Predecessor != nil {
		t.Fatalf("review continuation was lost or gained an interrupt handoff: %+v", s)
	}
	step(t, c)
	step(t, c)
	step(t, c)
	s := c.Status()
	if s.Active == nil || s.Active.Backend.Agent != "traex" || !strings.Contains(s.Active.Prompt, "read the review output") || s.Active.Predecessor != nil || n.created != 2 {
		t.Fatalf("new backend did not receive the queued continuation: %+v", s.Active)
	}
}

func TestFault_MaintenanceRestartReconcilesLostTurnReceipt(t *testing.T) {
	c, n, cfg, opts := fixture(t)
	command(t, c, "audit", protocol.MaintenanceRequest{})
	step(t, c)
	n.loseReply = true
	if err := c.Step(context.Background()); err == nil {
		t.Fatal("lost reply not preserved")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.lock = nil
	d, err := Open(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	command(t, d, "audit", protocol.MaintenanceRequest{})
	step(t, d)
	if n.created != 1 || n.started != 1 || d.Status().Active.TurnID != n.turn {
		t.Fatalf("duplicate execution: %+v", d.Status())
	}
	n.complete = true
	n.busy = false
	n.loseReply = false
	step(t, d)
	step(t, d)
	if n.created != 1 || n.started != 2 {
		t.Fatal("new evidence did not join original session")
	}
}

func TestMaintenanceSwitchPredecessorOnlyForInterrupt(t *testing.T) {
	for _, mode := range []string{"when-idle", "interrupt"} {
		t.Run(mode, func(t *testing.T) {
			c, n, _, _ := fixture(t)
			launch(t, c)
			b := c.Status().Backend
			b.Agent = "traex"
			command(t, c, "config", protocol.MaintenanceRequest{Backend: &b, Mode: mode})
			step(t, c)
			if n.created != 1 || c.Status().Backend.Agent != "codex" {
				t.Fatal("switched before native termination")
			}
			if mode == "interrupt" && n.interrupted != 1 {
				t.Fatal("did not interrupt")
			}
			n.busy = false
			n.complete = true
			step(t, c)
			step(t, c)
			command(t, c, "audit", protocol.MaintenanceRequest{})
			step(t, c)
			step(t, c)
			a := c.Status().Active
			if a.Backend.Agent != "traex" {
				t.Fatal("new backend not adopted")
			}
			if mode == "interrupt" {
				if a.Predecessor == nil || a.Predecessor.Agent != "codex" || a.Predecessor.SessionID != "native-1" || !strings.Contains(a.Prompt, "Previous agent: codex; session ID: native-1.") {
					t.Fatalf("missing minimal handoff: %+v", a)
				}
			} else if a.Predecessor != nil || strings.Contains(a.Prompt, "Previous agent") {
				t.Fatal("graceful switch leaked predecessor")
			}
		})
	}
}

func TestMaintenanceIntentDedupAndExclusiveController(t *testing.T) {
	c, n, cfg, opts := fixture(t)
	if d, err := Open(context.Background(), cfg, opts); err == nil {
		_ = d.Close()
		t.Fatal("second controller acquired ownership")
	}
	r := protocol.MaintenanceRequest{RequestID: "same", ExpectedRevision: c.Status().Revision}
	first, err := c.Command(context.Background(), "audit", r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Command(context.Background(), "audit", r)
	if err != nil || first != second {
		t.Fatal("idempotent retry changed result")
	}
	if _, err := c.Command(context.Background(), "pause", r); err == nil {
		t.Fatal("reused identity accepted changed action")
	}
	if n.created != 0 || n.started != 0 {
		t.Fatal("control handler executed native effects")
	}
}

func TestMaintenanceDetectionExcludesHealthyWaits(t *testing.T) {
	for _, status := range []string{"waiting", "starting", "running", "completed", "cancelled", "exhausted", "blocked", "unknown"} {
		v := protocol.IssueView{Manager: &protocol.IssueManagerView{Recovery: &protocol.AgentRecoveryView{Status: status}}}
		v.State = protocol.IssueActive
		v.IssueID = "sample"
		got := DetectIssue(v)
		want := status == "exhausted" || status == "blocked" || status == "unknown"
		if (len(got) > 0) != want {
			t.Errorf("%s candidates=%v", status, got)
		}
		v.State = protocol.IssuePaused
		if len(DetectIssue(v)) != 0 {
			t.Fatal("paused issue produced candidate")
		}
	}
}

func TestMaintenanceIndependentReviewIsSerializedInOriginalThread(t *testing.T) {
	c, n, _, _ := fixture(t)
	launch(t, c)
	command(t, c, "review", protocol.MaintenanceRequest{Base: "main", Head: strings.Repeat("a", 40)})
	step(t, c)
	if n.reviewed != 0 {
		t.Fatal("review overlapped repair turn")
	}
	n.busy = false
	n.complete = true
	step(t, c)
	step(t, c)
	if n.reviewed != 1 || n.created != 1 || c.Status().Review.Phase != "running" {
		t.Fatal("review did not retain the one native thread")
	}
	command(t, c, "audit", protocol.MaintenanceRequest{})
	step(t, c)
	if n.created != 1 || n.started != 2 {
		t.Fatal("audit overlapped review")
	}
	n.busy = false
	n.complete = true
	step(t, c)
	step(t, c)
	if n.reviewed != 1 || n.created != 1 || n.started != 3 || c.Status().Review.Phase != "completed" {
		t.Fatal("repair did not resume after review")
	}
}

func TestMaintenanceHistoryIsPagedOutsideCurrentSnapshot(t *testing.T) {
	c, n, cfg, opts := fixture(t)
	for range 4 {
		launch(t, c)
		n.busy = false
		n.complete = true
		step(t, c)
	}
	if len(c.Status().History) != 0 {
		t.Fatal("current snapshot retained all historical sessions")
	}
	first, next, more, err := c.History(context.Background(), 0, 2)
	if err != nil || len(first) != 2 || !more {
		t.Fatal(first, next, more, err)
	}
	second, _, more, err := c.History(context.Background(), next, 2)
	if err != nil || len(second) != 2 || more || first[1].ID == second[0].ID {
		t.Fatal(second, more, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.lock = nil
	d, err := Open(context.Background(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	items, _, more, err := d.History(context.Background(), 0, 100)
	if err != nil || len(items) != 4 || more {
		t.Fatal(items, more, err)
	}
}
