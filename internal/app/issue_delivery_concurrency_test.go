package app

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

type deliveryCase struct {
	issue             protocol.Issue
	source, execution string
	candidate         protocol.IssueDeliveryCandidate
	duty              protocol.IssueDeliveryDuty
}

func twoDeliveryCases(t *testing.T) (*harness, StartUpgrade, [2]deliveryCase) {
	t.Helper()
	h, upgrade := upgradeHarness(t)
	var cases [2]deliveryCase
	for n := range cases {
		_, i, source, c := deliveryFixtureOnHarness(t, h)
		if n == 1 {
			second := i.Plan.Deliveries[0]
			second.DeliveryID = "second-pr"
			second.Number = 18
			second.URL = "https://example.invalid/repo/pull/18"
			i.Plan.Deliveries = append(i.Plan.Deliveries, second)
			i = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerIssueUpdate{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
			c.URL = second.URL
			c.DeliveryID = second.DeliveryID
			c.HeadSHA = strings.Repeat("e", 40)
		}
		c.Deploy = true
		c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
		c.DeploymentScope = "controlled server and complete fleet update"
		observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
		d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
		answerDeliveryForTest(t, h, d, "approve", "")
		op := createDeliveryExecutionForTest(t, h, i.IssueID, d)
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var result taskResult
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(err)
		}
		cases[n] = deliveryCase{i, source, result.TaskID, c, d}
	}
	return h, upgrade, cases
}
func deliveryRunCount(t *testing.T, h *harness, task string) int {
	t.Helper()
	var n int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs WHERE task_id=?`, task).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeliveryTargetSerializesUnrelatedClaimsAndRetainsUnknownAcrossRestart(t *testing.T) {
	h, _, cases := twoDeliveryCases(t)
	hosts := []string{"linux-1", "linux-2"}
	envs := make([]*protocol.Envelope, 2)
	for n, host := range hosts {
		envs[n] = h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: host}, protocol.ClaimPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1}, nil)
	}
	var assignments [2]protocol.Assignment
	var claimed [2]bool
	var wg sync.WaitGroup
	for n := range hosts {
		wg.Add(1)
		go func() { defer wg.Done(); assignments[n], claimed[n] = h.claimWith(hosts[n], envs[n]) }()
	}
	wg.Wait()
	index := -1
	for n, ok := range claimed {
		if ok {
			if index != -1 {
				t.Fatal("two deployment runs claimed", assignments)
			}
			index = n
		}
	}
	if index < 0 {
		t.Fatal("no deployment claimed")
	}
	first := assignments[index]
	waiting := cases[0]
	if waiting.execution == first.TaskID {
		waiting = cases[1]
	}
	if deliveryRunCount(t, h, waiting.execution) != 0 || h.task(waiting.execution).Lifecycle != protocol.TaskQueued {
		t.Fatal("waiting Issue acquired a drain resource")
	}
	h.app = New(h.store, h.ids, h.clock.Now)
	if other, ok, _ := h.claim(hosts[1-index]); ok {
		t.Fatal("restart lost target ownership", other)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE runs SET observed_state='unknown' WHERE run_id=?`, first.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	task := h.task(first.TaskID)
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(first.TaskID), map[string]any{}, rev(task.ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if other, ok, _ := h.claim(hosts[1-index]); ok {
		t.Fatal("cancel guessed unknown execution stopped", other)
	}
	if h.run(first.RunID).ObservedState != protocol.RunUnknown || deliveryRunCount(t, h, waiting.execution) != 0 {
		t.Fatal("unknown identity was replaced")
	}
	if op := h.runAs(first, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "Original executor confirms its process stopped without starting an upgrade"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok, _ := h.claim(hosts[1-index])
	if !ok || next.TaskID != waiting.execution {
		t.Fatal("confirmed stop did not release target", next)
	}
	if h.run(first.RunID).ObservedState != protocol.RunFailed || h.task(first.TaskID).Lifecycle != protocol.TaskCancelled || h.count("upgrades") != 0 {
		t.Fatal("release rewrote original identity or installed a release")
	}
}

func TestDeliveryTargetBindsOneStartRejectsOldSourceAndFencesNextBaseline(t *testing.T) {
	h, upgrade, cases := twoDeliveryCases(t)
	first, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no first deployment")
	}
	selected := cases[0]
	waiting := cases[1]
	if first.TaskID != selected.execution {
		selected, waiting = waiting, selected
	}
	h.runAs(first, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if second, ok, _ := h.claim("linux-2"); ok {
		t.Fatal("second Issue held a run while waiting", second)
	}
	merged := strings.Repeat("c", 40)
	observed := selected.candidate
	observed.BaseSHA = merged
	observeDeliveryForTest(t, h, selected.issue.IssueID, observed, "merged", merged)
	upgrade.DeliveryTaskID = first.TaskID
	upgrade.ArtifactSources = map[string]release.SourceIdentity{}
	for _, a := range upgrade.Manifest.Artifacts {
		upgrade.ArtifactSources[a.SHA256] = release.SourceIdentity{Commit: merged}
	}
	old := upgrade
	old.ArtifactSources = map[string]release.SourceIdentity{old.ServerHash: {Commit: strings.Repeat("b", 40)}}
	expectWorkCode(t, startTestUpgrade(h, "old-source", old), protocol.CodeInvalidRequest)
	modified := upgrade
	modified.ArtifactSources = map[string]release.SourceIdentity{modified.ServerHash: {Commit: merged, Modified: true}}
	expectWorkCode(t, startTestUpgrade(h, "modified-source", modified), protocol.CodeInvalidRequest)
	if h.count("upgrades") != 0 {
		t.Fatal("old main installed")
	}
	if op := startTestUpgrade(h, "first-upgrade", upgrade); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	second := upgrade
	second.DeliveryTaskID = waiting.execution
	expectWorkCode(t, startTestUpgrade(h, "second-upgrade", second), protocol.CodeInvalidTransition)
	if h.count("upgrades") != 1 || deliveryRunCount(t, h, waiting.execution) != 0 {
		t.Fatal("parallel start acquired another target")
	}
	artifact := protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Started the one authorized controlled batch; installations pending", Evidence: []string{"upgrade:first-upgrade"}}}
	if op := h.runAs(first, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if second, ok, _ := h.claim("linux-2"); ok {
		t.Fatal("second execution/acceptance blocked the first drain", second)
	}
	batch := upgradeRead(h)
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	firstTask := h.task(first.TaskID)
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(first.TaskID), map[string]any{}, rev(firstTask.ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if second, ok, _ := h.claim("linux-2"); ok {
		t.Fatal("an old baseline approval survived the prior deployment", second)
	}
	if deliveryRunCount(t, h, waiting.execution) != 0 {
		t.Fatal("old candidate was replayed")
	}
	oldTask := h.task(waiting.execution)
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(waiting.execution), map[string]any{}, rev(oldTask.ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	fresh := waiting.candidate
	fresh.BaseSHA = merged
	fresh.HeadSHA = strings.Repeat("f", 40)
	fresh.Validation = "fresh rebased checks and known cancelled baseline"
	observeDeliveryForTest(t, h, waiting.issue.IssueID, fresh, "open", "")
	d := prepareDeliveryForTest(t, h, waiting.issue.IssueID, waiting.source, fresh)
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, waiting.issue.IssueID, d), protocol.CodeForbidden)
	answerDeliveryForTest(t, h, d, "approve", "")
	result := createDeliveryExecutionForTest(t, h, waiting.issue.IssueID, d)
	if result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	var next taskResult
	if err := json.Unmarshal(result.Result, &next); err != nil {
		t.Fatal(err)
	}
	admitted, ok, _ := h.claim("linux-2")
	if !ok || admitted.TaskID != next.TaskID {
		t.Fatal("fresh confirmed candidate could not continue", admitted)
	}
	if h.count("upgrades") != 1 || h.task(waiting.execution).Lifecycle != protocol.TaskCancelled {
		t.Fatal("recovery hid the failed original")
	}
}

func TestDeliveryTargetRejectsProviderQueryIssuedBeforeAnotherInstallation(t *testing.T) {
	h, upgrade := upgradeHarness(t)
	_, i, source, c := deliveryFixtureOnHarness(t, h)
	c.Deploy = true
	c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1", "linux-2"}}
	c.DeploymentScope = "controlled server and complete fleet"
	duty := prepareDeliveryForTest(t, h, i.IssueID, source, c)
	answerDeliveryForTest(t, h, duty, "approve", "")
	var rt protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		r, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-2")
		rt = r.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityDeliveryGitHub, protocol.CapabilityDeliveryCandidateIdentity)
	rt.Sequence++
	if op := h.versionedHello("linux-2", &rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assignments := pollDelivery(t, h, "linux-2", rt.InstanceID)
	if len(assignments) != 1 {
		t.Fatal(assignments)
	}
	old := assignments[0]
	// A local operator's separate batch may finish while the provider read is
	// still in flight. Its late response cannot become post-installation proof.
	if op := startTestUpgrade(h, "other-installation", upgrade); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	batch := upgradeRead(h)
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(batch.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	if accepted(t, reportDelivery(t, h, "linux-2", rt.InstanceID, old, "open", "")) {
		t.Fatal("late query crossed deployment baseline")
	}
	assignments = pollDelivery(t, h, "linux-2", rt.InstanceID)
	if len(assignments) != 1 || assignments[0].Generation <= old.Generation {
		t.Fatal("baseline refresh did not issue a new query", assignments)
	}
	a := assignments[0]
	payload := protocol.DeliveryReportPayload{InstanceID: rt.InstanceID, WorkItemID: i.IssueID, DeliveryID: c.DeliveryID, Generation: a.Generation, Nonce: a.Nonce, Snapshot: &protocol.DeliverySnapshot{State: "open", HeadSHA: c.HeadSHA, BaseSHA: c.BaseSHA, URL: c.URL}}
	if !accepted(t, h.exec(runner("linux-2"), h.env(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: "linux-2"}, payload, nil), ReportDelivery{})) {
		t.Fatal("fresh baseline facts rejected")
	}
	o := readObservations(t, h, i.IssueID)[0]
	if o.BaselineUpgradeID != "other-installation" || o.BaselineUpgradeRevision <= batch.View.Revision || o.RefreshPending {
		t.Fatal(o)
	}
	expired := deliveryDutyForTest(t, h, i.IssueID, source)
	if expired.Status != "preparing" || expired.Revision <= duty.Revision {
		t.Fatal("unchanged provider hid expired deployment baseline", expired)
	}
	current := canonicalTaskIssue(t, h, i.IssueID)
	var authErr error
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, authErr = deliveryExecutionAuthorized(t.Context(), tx.Q(), current, protocol.IssueDeliveryAuthorization{TaskID: source, Revision: duty.Revision})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if protocol.CodeOf(authErr) != protocol.CodeStaleRevision {
		t.Fatalf("old baseline answer survived: %v", authErr)
	}
	fresh := prepareDeliveryForTest(t, h, i.IssueID, source, c)
	if fresh.AttentionID == duty.AttentionID || fresh.Candidate.Deployment.BaselineUpgradeID != "other-installation" {
		t.Fatal(fresh)
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, fresh), protocol.CodeForbidden)
}

func TestDeliveryLegacyMaintenanceDoesNotAttributeModifiedBuildToExactCommit(t *testing.T) {
	h, upgrade := upgradeHarness(t)
	upgrade.ArtifactSources = map[string]release.SourceIdentity{upgrade.ServerHash: {Commit: strings.Repeat("c", 40), Modified: true}}
	if op := startTestUpgrade(h, "legacy-modified", upgrade); op.Status != protocol.OpSucceeded {
		t.Fatal("legacy operator permission changed", op)
	}
	if batch := upgradeRead(h); batch.View.SourceCommit != "" {
		t.Fatal("modified installation falsely attributed to exact source", batch.View)
	}
}
