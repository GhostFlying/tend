package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func stoppedRunWithPendingStartup(t *testing.T) (*server, *node, string, string) {
	t.Helper()
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, passing)
	n.log = runnerTestLog(t)
	n.heartbeat = 20 * time.Millisecond
	n.net.dropNext(protocol.OpRunsReportWorkspace, 10000)
	n.start()
	task := srv.createTask()
	var original string
	eventually(t, "offline work has a final retained result", func() bool {
		runs, err := n.runningJournal().Runs(t.Context())
		if err != nil || len(runs) != 1 {
			return false
		}
		original = runs[0].Assignment.RunID
		return runs[0].Phase == journal.PhaseSubmitting && runs[0].StoppedConfirmed
	})
	eventually(t, "original startup RPC remains pending", func() bool {
		n.net.mu.Lock()
		defer n.net.mu.Unlock()
		return n.net.drop[protocol.OpRunsReportWorkspace] < 9999
	})
	eventually(t, "server received physical exit while startup delivery was unavailable", func() bool {
		return srv.count(`SELECT COUNT(*) FROM run_logs WHERE run_id=? AND kind=?`, original, protocol.EventWorkerStopped) == 1
	})
	return srv, n, task, original
}

func TestFault_StartupReportRecoverySettlesOriginalResult(t *testing.T) {
	srv, n, task, original := stoppedRunWithPendingStartup(t)
	n.net.allow(protocol.OpRunsReportWorkspace)
	eventually(t, "startup fact sender has recovered", func() bool {
		reports, err := n.runningJournal().StartReports(t.Context(), original)
		return err == nil && len(reports) == 0
	})
	eventually(t, "final result settles after startup report recovery", func() bool {
		return srv.task(task).Lifecycle == protocol.TaskCompleted
	})
}

func TestFault_CancelSettlesWhileStartupReportRemainsUnavailable(t *testing.T) {
	srv, n, task, run := stoppedRunWithPendingStartup(t)
	rev := srv.task(task).ControlRevision
	env := protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "tasks.cancel", Target: protocol.Target{Type: "task", ID: task}, ExpectedControlRevision: &rev, Payload: json.RawMessage(`{}`)}
	if op, err := srv.app.Execute(t.Context(), owner, &env, app.CancelTask{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	eventually(t, "runner receives the original stop", func() bool {
		desired, err := n.runningJournal().ReceivedDesired(t.Context(), run)
		return err == nil && desired == protocol.DesiredStopped
	})
	eventually(t, "cancelled task releases the original Run despite the blocked startup endpoint", func() bool {
		return srv.count(`SELECT COUNT(*) FROM leases WHERE run_id=? AND status='released'`, run) == 1 && srv.task(task).Lifecycle == protocol.TaskCancelled
	})
}
