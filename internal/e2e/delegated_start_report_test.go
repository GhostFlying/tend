package e2e

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestFault_StartupReportRecoverySettlesOriginalResult(t *testing.T) {
	srv := newServer(t, time.Minute)
	n := newNode(t, srv, passing)
	n.log = runnerTestLog(t)
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
	n.net.allow(protocol.OpRunsReportWorkspace)
	eventually(t, "startup fact sender has recovered", func() bool {
		reports, err := n.runningJournal().StartReports(t.Context(), original)
		return err == nil && len(reports) == 0
	})
	eventually(t, "final result settles after startup report recovery", func() bool {
		return srv.task(task).Lifecycle == protocol.TaskCompleted
	})
}
