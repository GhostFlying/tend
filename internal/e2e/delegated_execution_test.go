package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
)

// The native peer speaks the normal app-server protocol. Only an actual
// second turn request can fix the workspace and submit the pull request.
func TestDelegatedNativePeer(t *testing.T) {
	service := os.Getenv("TEND_FIXTURE_PR_SERVICE")
	if service == "" {
		return
	}
	result := func() error {
		enc := json.NewEncoder(os.Stdout)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		var accept bool
		turn := 0
		finished := func(id string) error {
			return enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": id, "status": "completed"}}})
		}
		for scanner.Scan() {
			var req struct {
				ID     json.RawMessage
				Method string
				Params json.RawMessage
			}
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				return err
			}
			if req.Method == "" {
				if string(req.ID) == "5001" {
					if err := finished(fmt.Sprintf("turn-%d", turn)); err != nil {
						return err
					}
				}
				continue
			}
			if len(req.ID) == 0 {
				continue
			}
			var response any = map[string]any{}
			switch req.Method {
			case "thread/start":
				accept = bytes.Contains(req.Params, []byte(harness.VerdictTool))
				response = map[string]any{"thread": map[string]string{"id": "native-thread"}}
			case "turn/start":
				turn++
				response = map[string]any{"turn": map[string]string{"id": fmt.Sprintf("turn-%d", turn)}}
			}
			if err := enc.Encode(map[string]any{"id": req.ID, "result": response}); err != nil {
				return err
			}
			if req.Method != "turn/start" {
				continue
			}
			id := fmt.Sprintf("turn-%d", turn)
			tool := harness.ResultTool
			var args map[string]any
			switch {
			case accept:
				tool = harness.VerdictTool
				args = map[string]any{"verdict": "passed", "evidence": map[string]string{"tests": "green"}}
			case turn == 1:
				gate, err := http.Get(service + "/first-turn")
				if err != nil {
					return err
				}
				gate.Body.Close()
				if err := os.WriteFile("check.sh", []byte("test -f fixed\n"), 0600); err != nil {
					return err
				}
				if err := exec.Command("sh", "check.sh").Run(); err == nil {
					return fmt.Errorf("first check unexpectedly passed")
				}
				if err := enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "native-thread", "turnId": id, "item": map[string]string{"id": "red", "type": "agentMessage", "text": "test failed: fixed is missing"}}}); err != nil {
					return err
				}
				tool = harness.ContinueRunTool.Name
				args = map[string]any{"next_step": "Fix the failing check, rerun it, then open the pull request."}
			case turn == 2:
				if err := os.WriteFile("fixed", []byte("fixed"), 0600); err != nil {
					return err
				}
				if err := exec.Command("sh", "check.sh").Run(); err != nil {
					return err
				}
				pr, err := http.Post(service+"/pulls", "application/json", strings.NewReader(`{"title":"fix failing test","head":"repair"}`))
				if err != nil {
					return err
				}
				body, err := io.ReadAll(pr.Body)
				pr.Body.Close()
				if err != nil || pr.StatusCode != 201 {
					return fmt.Errorf("create PR: %s", pr.Status)
				}
				args = map[string]any{"report": map[string]any{"body": "test passed; created " + string(body)}}
			default:
				return fmt.Errorf("unexpected extra delegated turn %d", turn)
			}
			if err := enc.Encode(map[string]any{"id": 5001, "method": "item/tool/call", "params": map[string]any{"threadId": "native-thread", "turnId": id, "callId": "submit", "tool": tool, "arguments": args}}); err != nil {
				return err
			}
		}
		return scanner.Err()
	}()
	if result != nil {
		fmt.Fprintln(os.Stderr, result)
		os.Exit(1)
	}
	os.Exit(0)
}

type realWorkerWorkspace struct{ harness.Workers }

func (w realWorkerWorkspace) Start(ctx context.Context, s harness.WorkerSpec) (harness.Worker, error) {
	for _, dir := range []string{s.Dir, s.StateDir, s.ScratchDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	return w.Workers.Start(ctx, s)
}

func TestFault_DelegatedRunFinishesAcrossPartition(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%t", stop), func(t *testing.T) { partitionedNativeExecution(t, stop, false) })
	}
}
func TestFault_DelegatedResultRestartsOffline(t *testing.T) {
	partitionedNativeExecution(t, false, true)
}
func partitionedNativeExecution(t *testing.T, stop, restart bool) {
	clock := testkit.NewClock()
	ttl := time.Second
	srv := newServer(t, ttl, clock.Now)
	gate := make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(gate) }) }
	entered := make(chan struct{}, 1)
	var pulls atomic.Int32
	git := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/first-turn":
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-gate:
			case <-r.Context().Done():
			}
		case r.Method == "POST" && r.URL.Path == "/pulls":
			pulls.Add(1)
			w.WriteHeader(201)
			fmt.Fprint(w, "pull/1")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(git.Close)
	t.Cleanup(unblock)
	n := newNodeForKind(t, srv, protocol.WorkerAppServer, passing)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "app-server")
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$TEND_FIXTURE_EXECUTABLE\" -test.run='^TestDelegatedNativePeer$'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	n.backend = realWorkerWorkspace{traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: wrapper, Env: []string{"TEND_FIXTURE_EXECUTABLE=" + bin, "TEND_FIXTURE_PR_SERVICE=" + git.URL}, Stderr: os.Stderr}})}
	n.log = runnerTestLog(t)
	n.start()
	eventually(t, "runner advertises composable deliverables", func() bool { return srv.count(`SELECT COUNT(*) FROM hosts WHERE workspace_protocol >= 1`) == 1 })
	op := srv.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "repair the failing test and report the pull request", "spec": domain.Spec{Goal: "repair the failing test and open a pull request", Acceptance: []string{"tests pass and the pull request is reported"}, Deliverables: []string{"commit", "report"}, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}}}, app.CreateTask{})
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &created); err != nil || created.TaskID == "" {
		t.Fatal(op, err)
	}
	task := created.TaskID
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("original native turn never started")
	}
	n.net.set(true)
	eventually(t, "partition outside of all in-flight requests", func() bool { n.net.mu.Lock(); defer n.net.mu.Unlock(); return n.net.inflightRPC == 0 })
	clock.Advance(4 * ttl)
	if expired, err := srv.app.ExpireLeases(t.Context()); err != nil || expired != 0 {
		t.Fatal("delegated writer expired", expired, err)
	}
	unblock()
	eventually(t, "same local worker repairs the test and creates the PR offline", func() bool { return pulls.Load() == 1 })
	if got := srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=? AND protocol=7 AND role='implement'`, task); got != 1 {
		t.Fatal("original Run lost", got)
	}
	if got := srv.count(`SELECT COUNT(*) FROM artifacts WHERE task_id=?`, task); got != 0 {
		t.Fatal("offline result visible prematurely", got)
	}
	if restart {
		eventually(t, "physical exit and the original external result survive locally", func() bool {
			runs, err := n.runningJournal().Runs(t.Context())
			return err == nil && len(runs) == 1 && runs[0].StoppedConfirmed && runs[0].Phase == journal.PhaseSubmitting
		})
		n.stop()
		n.start()
	}

	if stop {
		eventually(t, "native result receipt is durable in the offline journal", func() bool {
			runs, err := n.runningJournal().Runs(t.Context())
			if err != nil || len(runs) != 1 {
				return false
			}
			events, err := n.runningJournal().PendingEvents(t.Context(), runs[0].Assignment.RunID, 100)
			if err != nil {
				return false
			}
			for _, e := range events {
				if e.Kind == protocol.EventWorkerSubmitted && bytes.Contains(e.Body, []byte("pull/1")) {
					return true
				}
			}
			return false
		})
		revision := srv.task(task).ControlRevision
		env := protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "tasks.cancel", Target: protocol.Target{Type: "task", ID: task}, ExpectedControlRevision: &revision, Payload: json.RawMessage(`{}`)}
		if op, err := srv.app.Execute(t.Context(), owner, &env, app.CancelTask{}); err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		status := func() *protocol.RunSyncObservation {
			var d protocol.TaskDetail
			err := srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				d, err = views.Task(t.Context(), tx.Q(), task)
				return err
			})
			if err != nil || len(d.Runs) != 1 {
				t.Fatal("read cancelled Run", err, len(d.Runs))
			}
			return d.Runs[0].Observation
		}
		if o := status(); o.StopRequestedAt == 0 || o.StoppedAt != 0 {
			t.Fatal("stop was presented as confirmed while offline", o)
		}
		n.net.dropNext(protocol.OpRunnerSyncEvents, 10000)
		n.net.dropNext(protocol.OpRunsRecordSessionStop, 10000)
		n.net.set(false)
		eventually(t, "original business ownership settles before physical stop delivery", func() bool {
			return srv.count(`SELECT COUNT(*) FROM leases l JOIN runs r USING(run_id) WHERE r.task_id=? AND l.status='released'`, task) == 1
		})
		n.net.allow(protocol.OpRunsRecordSessionStop)
		eventually(t, "runner confirms physical exit while its outcome is still queued", func() bool { return status().StoppedAt > 0 })
		n.net.allow(protocol.OpRunnerSyncEvents)
		eventually(t, "cancelled Run retains its already created PR", func() bool {
			return srv.count(`SELECT COUNT(*) FROM run_logs WHERE kind='worker.result_submitted' AND body_json LIKE '%pull/1%'`) == 1
		})
		if got := srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=? AND role='accept'`, task); got != 0 || pulls.Load() != 1 {
			t.Fatal("withdrawn Run scheduled acceptance or duplicated the PR", got, pulls.Load())
		}
		return
	}
	n.net.set(false)
	eventually(t, "implementation and independent acceptance settle", func() bool { return srv.task(task).Lifecycle == protocol.TaskCompleted })
	eventually(t, "original worker outcome reaches the server", func() bool {
		return srv.count(`SELECT COUNT(*) FROM run_logs WHERE kind='worker.outcome' AND body_json LIKE '%pull/1%'`) == 1
	})
	if got := srv.count(`SELECT COUNT(*) FROM artifacts WHERE task_id=?`, task); got != 1 || pulls.Load() != 1 {
		t.Fatal("duplicate artifact or pull request", got, pulls.Load())
	}
}
