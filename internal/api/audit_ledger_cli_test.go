package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/maintenance"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestAuditLedgerQueriesThroughOwnerCLI(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	normalTask := e.task("Normal wait")
	unconfirmedTask := e.task("Stop confirmation unavailable")
	dependency := e.task("Dependency still open")
	historicalRun := e.ids.New(protocol.PrefixRun)
	normalRun := e.startRun(normalTask)
	unconfirmedRun := e.startRun(unconfirmedTask)
	now := e.clock.Now().UnixMilli()

	// Seed responsibility, run protocol and historical wait. The two current
	// waits below go through the actual wait and physical-yield operations.
	err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, task := range []string{normalTask, unconfirmedTask, dependency} {
			if _, err := tx.Exec(t.Context(), `UPDATE task_work_item_owners SET work_item_id=? WHERE task_id=?`, issue.IssueID, task); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(t.Context(), `UPDATE runs SET protocol=3, observed_state='running' WHERE run_id IN (?,?)`, normalRun, unconfirmedRun); err != nil {
			return err
		}
		for _, run := range []string{normalRun, unconfirmedRun} {
			if _, err := tx.Exec(t.Context(), `UPDATE plan_actions SET status='dispatched' WHERE action_id=(SELECT action_id FROM runs WHERE run_id=?)`, run); err != nil {
				return err
			}
			if err := tx.InsertLease(t.Context(), store.LeaseRow{LeaseID: e.ids.New(protocol.PrefixLease), RunID: run, HostID: "host-a", ExpiresAt: now + 60_000}, now); err != nil {
				return err
			}
		}
		originalRun, err := store.GetRun(t.Context(), tx.Q(), normalRun)
		if err != nil {
			return err
		}
		historicalAction, err := store.GetAction(t.Context(), tx.Q(), originalRun.ActionID)
		if err != nil {
			return err
		}
		historicalAction.ActionID, historicalAction.EffectKey = e.ids.New(protocol.PrefixAction), "previous-attempt"
		historicalAction.Status = protocol.ActionSuperseded
		if err := tx.InsertAction(t.Context(), historicalAction); err != nil {
			return err
		}
		originalRun.RunID, originalRun.ActionID = historicalRun, historicalAction.ActionID
		originalRun.ObservedState, originalRun.DesiredState = protocol.RunFinished, protocol.DesiredStopped
		if err := tx.InsertRun(t.Context(), originalRun); err != nil {
			return err
		}
		return tx.PutExternalWait(t.Context(), protocol.ExternalWait{
			WaitExternalPayload: protocol.WaitExternalPayload{Kind: "task", TargetID: dependency, WaitKey: "old", Reason: "Previous wait", Recovery: "Read the previous outcome"},
			WaitID:              e.ids.New(protocol.PrefixRequest), RunID: historicalRun, TaskID: normalTask, Status: "continued", Stopped: true, CreatedAtMillis: now,
		}, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	runOp := func(runID, name string, payload any, command app.Command) protocol.Operation {
		t.Helper()
		envelope := e.envelope(name, protocol.Target{Type: "run", ID: runID}, nil, payload)
		p := protocol.Principal{Kind: protocol.PrincipalRun, Subject: runID, Role: protocol.RoleImplement}
		result, err := e.app.Execute(t.Context(), p, &envelope, command)
		if err != nil || result.Status != protocol.OpSucceeded {
			t.Fatalf("%s: %+v, %v", name, result, err)
		}
		return result
	}
	waitPayload := protocol.WaitExternalPayload{
		Kind: "task", TargetID: dependency, WaitKey: "dependency-open", Reason: "Await original dependency", Recovery: "Read its state before continuing",
	}
	normal := runOp(normalRun, protocol.OpRunsWaitExternal, waitPayload, app.WaitExternal{})
	runOp(normalRun, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: normal.RequestID}, app.YieldExternalWait{})
	runOp(unconfirmedRun, protocol.OpRunsWaitExternal, waitPayload, app.WaitExternal{})
	// No stop confirmation follows the unconfirmed wait. Supply the original
	// runner's diagnostic as an input to the production log ingestion path.
	p := protocol.Principal{Kind: protocol.PrincipalRun, Subject: unconfirmedRun, Role: protocol.RoleImplement}
	if _, err := e.app.RecordRunEvents(t.Context(), p, unconfirmedRun, []protocol.RunEvent{
		{Seq: 1, Kind: "runner.diagnostic", Body: json.RawMessage(`{"close_status":"unknown","reason":"no stop receipt returned"}`)},
	}); err != nil {
		t.Fatal(err)
	}

	c := newCapabilityCLI(t, e)
	before := e.count("SELECT COUNT(*) FROM events")
	fullIssue := c.run("issue", "show", issue.IssueID, "--json")
	var view protocol.IssueView
	decodeAuditJSON(t, fullIssue, &view)
	if len(view.Tasks) != 3 || len(maintenance.DetectIssue(view)) != 0 {
		t.Fatalf("fixture should expose all three Tasks and no detector case: %+v", view.Tasks)
	}
	narrowIssue := c.run("issue", "show", issue.IssueID, "--fields", "issue_id,effective_work_status,manager,tasks")
	assertAuditFields(t, fullIssue, narrowIssue, "issue_id", "effective_work_status", "manager", "tasks")

	fullTask := c.run("task", "show", unconfirmedTask, "--json")
	narrowTask := c.run("task", "show", unconfirmedTask, "--fields", "task_id,lifecycle,execution,runs,artifacts,rating,workspace_state")
	assertAuditFields(t, fullTask, narrowTask, "task_id", "lifecycle", "execution", "runs", "artifacts", "rating", "workspace_state")
	if !strings.Contains(string(narrowTask), `"artifacts": null`) || !strings.Contains(string(narrowTask), `"rating": ""`) ||
		strings.Contains(string(narrowTask), `"workspace_state"`) {
		t.Fatalf("explicit null, empty string or omitted field changed: %s", narrowTask)
	}

	oldIssue := c.run("issue", "show", issue.IssueID, "--revision", "1", "--json")
	narrowOldIssue := c.run("issue", "show", issue.IssueID, "--revision", "1", "--fields", "issue_id,revision,historical,manager")
	assertAuditFields(t, oldIssue, narrowOldIssue, "issue_id", "revision", "historical", "manager")
	if code, _ := c.call("issue", "show", issue.IssueID, "--limit", "4"); code == 0 {
		t.Fatal("issue show accepted ignored --limit")
	}

	for _, check := range []struct {
		command []string
		flags   []string
	}{
		{[]string{"issue", "show"}, []string{"--fields", "--revision"}},
		{[]string{"task", "show"}, []string{"--fields"}},
		{[]string{"task", "waits"}, []string{"--run", "--status", "cancelled", "no pagination"}},
		{[]string{"issue", "activity"}, []string{"--before", "--notification", "--after", "next_before"}},
		{[]string{"issue", "deliveries"}, []string{"--json", "issue refresh-delivery"}},
	} {
		help := string(c.run(append(check.command, "--help")...))
		for _, flag := range check.flags {
			if !strings.Contains(help, flag) {
				t.Errorf("%v help missing %q", check.command, flag)
			}
		}
		for _, flag := range []string{"--limit", "--data", "--request-id", "--dry-run"} {
			if strings.Contains(help, flag) {
				t.Errorf("%v help lists unsupported %q", check.command, flag)
			}
		}
	}

	all := c.run("task", "waits", normalTask)
	var waits, selected []protocol.ExternalWait
	decodeAuditJSON(t, all, &waits)
	if len(waits) != 2 || waits[0].RunID != historicalRun || waits[1].RunID != normalRun || !waits[1].Stopped || waits[1].Status != "waiting" {
		t.Fatalf("normal wait and history: %+v", waits)
	}
	byRun := c.run("task", "waits", normalTask, "--run", normalRun)
	decodeAuditJSON(t, byRun, &selected)
	if !reflect.DeepEqual(selected, waits[1:]) {
		t.Fatalf("exact Run filter changed the original wait: %+v", selected)
	}
	if both := c.run("task", "waits", normalTask, "--run", normalRun, "--status", "waiting"); string(both) != string(byRun) {
		t.Fatalf("combined query differs from the exact match: %s", both)
	}
	for _, args := range [][]string{
		{"--run", unconfirmedRun}, {"--run", normalRun, "--status", "stopping"},
	} {
		if empty := c.run(append([]string{"task", "waits", normalTask}, args...)...); string(empty) != "[]\n" {
			t.Fatalf("unmatched query %v returned %s", args, empty)
		}
	}

	missed := c.run("task", "waits", unconfirmedTask, "--status", "stopping")
	decodeAuditJSON(t, missed, &selected)
	if len(selected) != 1 || selected[0].RunID != unconfirmedRun || selected[0].Stopped || selected[0].TargetID != dependency {
		t.Fatalf("unconfirmed stop facts lost: %+v", selected)
	}
	original := c.run("run", "logs", selected[0].RunID, "--json")
	if !strings.Contains(string(original), `"close_status": "unknown"`) || !strings.Contains(string(original), "no stop receipt returned") {
		t.Fatalf("original diagnostic did not retain unknown: %s", original)
	}

	path := "/api/v1/tasks/" + normalTask + "/external-waits"
	if got := e.do("GET", path+"?run_id="+normalRun, "", nil, nil); got != http.StatusUnauthorized {
		t.Fatalf("wait authentication changed: HTTP %d", got)
	}
	for _, query := range []string{"run_id=", "status=bogus", "status=waiting&status=waiting"} {
		if got := e.do("GET", path+"?"+query, token, nil, nil); got != http.StatusBadRequest {
			t.Fatalf("invalid query %s: HTTP %d", query, got)
		}
	}
	if after := e.count("SELECT COUNT(*) FROM events"); after != before {
		t.Fatalf("audit reads appended events: %d to %d", before, after)
	}

	fullMissed := c.run("task", "waits", unconfirmedTask)
	fullBytes := len(fullIssue) + len(fullTask) + len(all) + len(fullMissed) + len(original)
	narrowBytes := len(narrowIssue) + len(narrowTask) + len(byRun) + len(missed) + len(original)
	if narrowBytes >= fullBytes {
		t.Fatalf("selected CLI output did not shrink: %d to %d bytes", fullBytes, narrowBytes)
	}
	t.Logf("CLI stdout bytes (full → selected): Issue %d → %d, Task %d → %d, normal waits %d → %d, unconfirmed wait %d → %d, original log %d → %d, total %d → %d",
		len(fullIssue), len(narrowIssue), len(fullTask), len(narrowTask), len(all), len(byRun), len(fullMissed), len(missed), len(original), len(original), fullBytes, narrowBytes)
	// Exercise an explicit unknown in the show projection independently of
	// the earlier fixture, where the probe saw a still-running original.
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.UpdateRunObserved(t.Context(), unconfirmedRun, protocol.RunUnknown, now)
	}); err != nil {
		t.Fatal(err)
	}
	fullUnknown := c.run("task", "show", unconfirmedTask, "--json")
	narrowUnknown := c.run("task", "show", unconfirmedTask, "--fields", "execution,runs")
	assertAuditFields(t, fullUnknown, narrowUnknown, "execution", "runs")
	if !strings.Contains(string(narrowUnknown), `"observed": "unknown"`) {
		t.Fatalf("unknown Run observation lost: %s", narrowUnknown)
	}

}

func decodeAuditJSON(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func assertAuditFields(t *testing.T, full, narrow []byte, fields ...string) {
	t.Helper()
	var all, got map[string]any
	decodeAuditJSON(t, full, &all)
	decodeAuditJSON(t, narrow, &got)
	want := map[string]any{}
	for _, field := range fields {
		if value, ok := all[field]; ok {
			want[field] = value
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected fields %+v, want %+v", got, want)
	}
}
