package app

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func concurrencyTickWorkFixture(t *testing.T, active, history int) *harness {
	t.Helper()
	h, runtime, projection, hash := inventorySetup(t)
	config := testConfig()
	config.Hosts[0].Slots = 128
	h.syncConfig(config)
	if op := h.exec(runner("linux-1"), inventoryRequest(h, runtime, projection, hash, 1, 0, "first"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityAdaptiveConcurrency)
	runtime.JournalVersion = 12
	for _, agent := range projection.Agents {
		runtime.AgentExecutors = append(runtime.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: 1, SnapshotID: "first", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, group := range []string{"shared", "other"} {
		concurrencyConfigure(t, h, "create", group, 0, protocol.ConfigureConcurrencyPayload{Name: group, Min: 1, Initial: 128, Max: 128})
	}
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: "linux-1", RuntimeID: "worker"})
	for range history {
		_, acceptance := h.toAwaitingAcceptance()
		if op := h.submitVerdict(acceptance, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	for i := 0; i < active; i++ {
		h.createTask(owner, createTaskPayload{Title: fmt.Sprintf("active %d", i), Spec: spec})
		runtime.Sequence++
		op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil), Claim{})
		var result claimResult
		if err := json.Unmarshal(op.Result, &result); err != nil || result.Assignment.RunID == "" {
			t.Fatalf("claim %d: %+v %v", i, op, err)
		}
		run := result.Assignment.RunID
		if op := h.exec(runPrincipal(run, protocol.RoleImplement), h.env("runs.report_started", runTarget(run), nil, nil), ReportRunStarted{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		body, err := json.Marshal(protocol.ConcurrencyFeedback{Kind: "running", SessionID: fmt.Sprintf("native-%d", i), TurnID: "turn"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(run, protocol.RoleImplement), run, []protocol.RunEvent{{Seq: 1, Kind: "worker.concurrency", Body: body}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.app.tickConcurrency(t.Context()); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestConcurrencySyncSettledWorkIsBounded(t *testing.T) {
	h := concurrencyTickWorkFixture(t, 4, 0)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TEMP TABLE settled_group_writes(n INTEGER NOT NULL); INSERT INTO settled_group_writes VALUES(0);
CREATE TEMP TRIGGER count_settled_group AFTER UPDATE ON concurrency_groups BEGIN UPDATE settled_group_writes SET n=n+1; END;`)
		if err != nil {
			return err
		}
		var before string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT state_json FROM concurrency_groups WHERE group_id='shared'`).Scan(&before); err != nil {
			return err
		}
		if err := syncConcurrencyExecutions(t.Context(), tx, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		var writes int
		var after string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT n FROM settled_group_writes`).Scan(&writes); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT state_json FROM concurrency_groups WHERE group_id='shared'`).Scan(&after); err != nil {
			return err
		}
		if before != after {
			t.Errorf("settled sync changed group state: before=%s after=%s", before, after)
		}
		if writes > 1 {
			t.Errorf("unchanged executions resampled the same group %d times, want at most 1", writes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrencySyncHeartbeatWorkIsBounded(t *testing.T) {
	h := concurrencyTickWorkFixture(t, 4, 2)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TEMP TABLE heartbeat_sync_writes(kind TEXT PRIMARY KEY,n INTEGER NOT NULL);
INSERT INTO heartbeat_sync_writes VALUES('group',0),('execution',0);
CREATE TEMP TRIGGER count_heartbeat_group AFTER UPDATE ON concurrency_groups BEGIN UPDATE heartbeat_sync_writes SET n=n+1 WHERE kind='group'; END;
CREATE TEMP TRIGGER count_heartbeat_execution AFTER UPDATE ON concurrency_executions BEGIN UPDATE heartbeat_sync_writes SET n=n+1 WHERE kind='execution'; END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, heartbeat := range []bool{false, true} {
		if heartbeat {
			h.clock.Advance(time.Second)
			if _, err := h.heartbeat(t.Context(), runner("linux-1"), nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE heartbeat_sync_writes SET n=0`); err != nil {
				return err
			}
			started := time.Now()
			if err := syncConcurrencyExecutions(t.Context(), tx, h.clock.Now().UnixMilli()); err != nil {
				return err
			}
			elapsed := time.Since(started)
			var groups, executions int
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT n FROM heartbeat_sync_writes WHERE kind='group'`).Scan(&groups); err != nil {
				return err
			}
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT n FROM heartbeat_sync_writes WHERE kind='execution'`).Scan(&executions); err != nil {
				return err
			}
			var stale int
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM concurrency_executions WHERE state='running' AND last_seen<?`, h.clock.Now().UnixMilli()).Scan(&stale); err != nil {
				return err
			}
			t.Logf("active=4 history=2 heartbeat=%t sync=%s group_updates=%d execution_updates=%d", heartbeat, elapsed, groups, executions)
			wantGroups := 0
			if heartbeat {
				wantGroups = 1
			}
			if groups != wantGroups || stale != 0 {
				t.Errorf("heartbeat=%t resampled unchanged load: group_updates=%d want%d stale_liveness=%d want0", heartbeat, groups, wantGroups, stale)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
