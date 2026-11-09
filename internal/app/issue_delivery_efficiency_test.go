package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// The existing fixture supplies one real whole-PR verdict and its continuing
// implementation Run. Only provider observations and physical installation
// receipts are fake; context, approval, binding, results and settlement use
// their normal application paths.
func TestIssueDeliveryEfficiencyKeepsOneReviewExecutionAndHistoricalInstallation(t *testing.T) {
	h, runtime, issue, impl, candidate := liveReviewedDeliveryFixture(t)
	tasks, runs, acceptances := h.count("tasks"), h.count("runs"), h.count("acceptances")
	readContext := func(cursor string) protocol.IssueContextIncremental {
		t.Helper()
		value, err := h.app.ReadIssueContext(t.Context(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: cursor, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		page, ok := value.(protocol.IssueContextIncremental)
		if !ok || page.IssueID != issue.IssueID || page.NextCursor == "" || page.ContentID == "" || page.More {
			t.Fatalf("bounded combination fixture lost its context checkpoint: %#v", value)
		}
		return page
	}
	snapshot := readContext("")
	var reviewID string
	sawTask, sawWorking := false, false
	for _, item := range snapshot.Items {
		if item.Working != nil {
			sawWorking = item.Working.Issue.IssueID == issue.IssueID
		}
		if item.Task != nil && item.Task.TaskID == impl.TaskID {
			sawTask = item.Task.Lifecycle == protocol.TaskRunning
		}
		if item.Review != nil {
			if reviewID != "" {
				t.Fatal("snapshot duplicated the same whole-PR responsibility")
			}
			reviewID = item.Review.ReviewID
		}
	}
	if snapshot.Kind != "snapshot" || !sawTask || !sawWorking || reviewID == "" {
		t.Fatalf("new session cannot discover its current work and original review: %+v", snapshot)
	}
	quiet := readContext(snapshot.NextCursor)
	if quiet.Kind != "delta" || len(quiet.Items) != 0 || h.count("tasks") != tasks || h.count("runs") != runs || h.count("acceptances") != acceptances {
		t.Fatal("reading unchanged context repeated work", quiet)
	}

	// Automatic PR integration does not grant deployment. The real candidate
	// question must be answered before this same live Task can bind execution.
	candidate.ProviderState, candidate.MergeSHA = "merged", strings.Repeat("c", 40)
	candidate.TargetSHA = candidate.MergeSHA
	candidate.Deploy = true
	candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	candidate.DeploymentScope = "Install and verify the original reviewed delivery on its explicit server and runner"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		observation, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, candidate.DeliveryID)
		if err != nil {
			return err
		}
		// Preserve the fixture's real exact-head CI and independent review when
		// the fake provider records its merged result.
		observation.Observation.State, observation.Observation.MergeSHA = "merged", candidate.MergeSHA
		observation.Observation.TargetSHA, observation.Observation.TargetContainsMerge = candidate.TargetSHA, true
		return putDeliveryObservationForTest(tx, t.Context(), observation)
	}); err != nil {
		t.Fatal(err)
	}
	duty := prepareLiveDeliveryForTest(t, h, issue.IssueID, impl.TaskID, candidate)
	bind := protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: impl.TaskID, ExecutionTaskID: impl.TaskID, ExpectedTaskControlRevision: rev(h.task(impl.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Keep the actual approved delivery inside its continuing implementation Task"}}
	executeDelivery := func(p protocol.UpdateIssueDeliveryPayload) (protocol.Operation, error) {
		t.Helper()
		current := canonicalTaskIssue(t, h, issue.IssueID)
		p.ExpectedAssignmentEpoch = current.AssignmentEpoch
		return h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(current.Revision)), UpdateIssueDelivery{})
	}
	operationCode := func(op protocol.Operation, err error) protocol.Code {
		if op.Error != nil {
			return op.Error.Code
		}
		return protocol.CodeOf(err)
	}
	if op, err := executeDelivery(bind); operationCode(op, err) != protocol.CodeForbidden || h.count("issue_delivery_executions") != 0 {
		t.Fatal("automatic merge policy did not preserve the original deployment approval", op, err)
	}
	answerDeliveryForTest(t, h, duty, "approve", "")
	if op := updateDeliveryForTest(t, h, issue.IssueID, bind); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("tasks") != tasks || h.count("runs") != runs || h.count("acceptances") != acceptances {
		t.Fatal("approved binding created an artificial stage Task or Run")
	}

	hash := strings.Repeat("e", 64)
	batch := store.UpgradeRow{DeliveryTaskID: impl.TaskID, View: protocol.UpgradeView{UpgradeID: "efficiency-original-install", ReleaseID: "original-release", SourceCommit: candidate.TargetSHA, Revision: 21, Status: "attention", Phase: "checking", Reason: "Physical replacement receipt is still unknown", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(), Hosts: []protocol.UpgradeHostView{{HostID: "linux-1", Status: "skipped"}}}, Manifest: release.Manifest{ReleaseID: "original-release"}, ManifestSHA256: hash, ServerHash: hash, ServerInstance: "server", Hosts: []store.UpgradeHost{{HostID: "linux-1", PreviousHash: hash, TargetHash: hash}}}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := checkDeliveryUpgradeStart(t.Context(), &Exec{Now: h.clock.Now()}, tx.Q(), StartUpgrade{DeliveryTaskID: impl.TaskID, ServerInstance: "server", ServerHash: hash, ArtifactSources: map[string]release.SourceIdentity{hash: {Commit: candidate.TargetSHA}}})
		return err
	}); err != nil {
		t.Fatal("approved running delivery could not admit its original installation", err)
	}
	putBatch := func() {
		t.Helper()
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
			t.Fatal(err)
		}
	}
	putBatch()
	artifact := testArtifact(h.task(impl.TaskID).WorkspaceName)
	artifact.Mounts[0].CommitSHA = candidate.HeadSHA
	artifact.Report = &protocol.ResultReport{Body: "Existing candidate checks: make check; exit 0. The original controlled installation still awaits its physical receipt.", Evidence: []string{"command: make check", "exit: 0", "head: " + candidate.HeadSHA, "upgrade:" + batch.View.UpgradeID}, Uncertainties: []string{"The reported checks alone do not prove the installation or independent acceptance"}}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifactID := h.task(impl.TaskID).CurrentArtifactID
	if pending, ok := h.claimInputRun(runtime); ok || h.count("runs") != runs || h.count("acceptances") != acceptances {
		t.Fatal("unknown physical installation started acceptance or repeated execution", pending)
	}
	if op, err := executeDelivery(protocol.UpdateIssueDeliveryPayload{Action: "complete", TaskID: impl.TaskID, UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Read the original installation while its actual replacement receipt remains unknown"}}); operationCode(op, err) != protocol.CodeInvalidTransition {
		t.Fatal("existing checks were promoted into installation and acceptance proof", op, err)
	}

	// The fake controller later proves that original batch. It does not start a
	// replacement installation, create another Task or rewrite the submitted log.
	batch.View.Status, batch.View.Phase, batch.View.Reason = "completed", "completed", ""
	batch.View.Revision++
	batch.ServerProcess, batch.ServerHealthy = "original-installed-process", 2
	putBatch()
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.TaskID != impl.TaskID || accept.Role != protocol.RoleAccept || h.count("tasks") != tasks || h.count("runs") != runs+1 || h.count("upgrades") != 1 {
		t.Fatal("confirmation did not release exactly the original independent acceptance", accept)
	}
	// The independent reviewer discovers bounded evidence on the ordinary run
	// context surface, then reads the immutable original only when it needs it.
	context, err := h.app.ReadRunContext(t.Context(), runPrincipal(accept.RunID, accept.Role), protocol.IssueMaterialsQuery{Kind: "reviews", ID: candidate.DeliveryID})
	if err != nil || context.Materials.SharedReviews == nil {
		t.Fatal("ordinary reviewer cannot discover the candidate's existing checks", context, err)
	}
	page := context.Materials.SharedReviews
	indexed, err := json.Marshal(page)
	if err != nil || strings.Contains(string(indexed), artifact.Report.Body) {
		t.Fatal("shared review index repeated the original report body", err)
	}
	if len(page.Items) != 1 || page.Items[0].ReviewID != reviewID || !page.Items[0].Valid || len(page.Items[0].Rounds) != 1 || len(page.ExistingEvidence) != 1 {
		t.Fatal("evidence discovery repeated or replaced the original whole-PR review", page)
	}
	group := page.ExistingEvidence[0]
	if group.ReviewID != reviewID || group.HeadSHA != candidate.HeadSHA || group.BaseSHA != candidate.BaseSHA || group.ContributorCount != 2 || group.ContributionsMore {
		t.Fatal("shared review lost its exact candidate or original contributors", group)
	}
	var originalQuery *protocol.IssueMaterialsQuery
	reportHash := sha256.Sum256([]byte(artifact.Report.Body))
	for _, contribution := range group.Contributions {
		if contribution.TaskID != impl.TaskID {
			continue
		}
		if contribution.ArtifactID != artifactID || contribution.RunID != impl.RunID || contribution.CandidateRelation != "head_matches" || contribution.Report == nil || contribution.Report.BodySHA256 != hex.EncodeToString(reportHash[:]) || contribution.Report.BodyBytes != len(artifact.Report.Body) || contribution.Report.EvidenceCount != len(artifact.Report.Evidence) || contribution.Report.UncertaintyCount != len(artifact.Report.Uncertainties) || !slices.Contains(contribution.MissingStructuredFields, "check_exit_codes") {
			t.Fatal("evidence index lost original identity or promoted prose into typed success", contribution)
		}
		originalQuery = contribution.ReadQuery
	}
	if originalQuery == nil || originalQuery.Kind != "artifacts" || originalQuery.ID != artifactID {
		t.Fatal("candidate evidence has no original artifact read path", group)
	}
	original, err := h.app.ReadRunContext(t.Context(), runPrincipal(accept.RunID, accept.Role), *originalQuery)
	if err != nil || len(original.Materials.Artifacts) != 1 || original.Materials.Artifacts[0].Artifact.ArtifactID != artifactID {
		t.Fatal("ordinary reviewer cannot read the referenced original", original, err)
	}
	var retained protocol.ArtifactPayload
	if err := json.Unmarshal(original.Materials.Artifacts[0].Artifact.Ref, &retained); err != nil || !reflect.DeepEqual(retained.Report, artifact.Report) {
		t.Fatal("on-demand evidence changed the submitted check log or uncertainty", retained, err)
	}
	if h.count("tasks") != tasks || h.count("runs") != runs+1 || h.count("acceptances") != acceptances || h.count("upgrades") != 1 || h.count("shared_delivery_reviews") != 1 {
		t.Fatal("discovering or reading existing evidence repeated work")
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"reuse":"Reviewed the original candidate checks and verified the original installation receipt"}`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(impl.TaskID)
	if !h.releaseResource(impl.TaskID, resource.WorkspaceID) {
		t.Fatal("release original accepted resource")
	}
	if h.count("acceptances") != acceptances+1 {
		t.Fatal("the original result received duplicate acceptance")
	}

	advanceHistoricalDeploymentFacts(t, h, issue.IssueID, impl.TaskID, batch, false)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		authority, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), impl.TaskID)
		if err == nil && (authority.Valid || authority.Settlement == nil || !authority.Settlement.Eligible || authority.Settlement.UpgradeID != batch.View.UpgradeID || authority.Settlement.ArtifactID != artifactID || authority.Settlement.TargetSHA != candidate.TargetSHA) {
			t.Fatal("maintenance erased historical proof or restored new-install permission", authority)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{"tasks": h.count("tasks"), "runs": h.count("runs"), "upgrades": h.count("upgrades"), "acceptances": h.count("acceptances")}
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "complete", TaskID: impl.TaskID, UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "The owner settles the accepted original installation after independent maintenance"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for table, before := range counts {
		if h.count(table) != before {
			t.Fatalf("historical settlement repeated %s", table)
		}
	}
	if h.count("upgrades") != 2 || deliveryDutyForTest(t, h, issue.IssueID, impl.TaskID).Status != "complete" {
		t.Fatal("original delivery did not settle alongside exactly one later maintenance")
	}
	delta := readContext(quiet.NextCursor)
	if delta.Kind != "delta" || len(delta.Items) == 0 {
		t.Fatal("owner could not incrementally discover the completed responsibility", delta)
	}
	sawTask, sawDelivery := false, false
	seen := map[string]bool{}
	for _, item := range delta.Items {
		key := item.Kind + "/" + item.ID
		if seen[key] {
			t.Fatal("incremental refresh repeated the same changed object", key)
		}
		seen[key] = true
		if item.Task != nil && item.Task.TaskID == impl.TaskID {
			sawTask = item.Task.Lifecycle == protocol.TaskCompleted
		}
		if entry := item.Delivery; entry != nil && entry.ExecutionTaskID == impl.TaskID {
			sawDelivery = entry.Status == "complete" && entry.Candidate != nil && entry.Candidate.Validation == "" && entry.Candidate.DeploymentScope == ""
		}
		if item.Review != nil && item.Review.ReviewID != reviewID {
			t.Fatal("context refresh invented another whole-PR review", item.Review)
		}
	}
	if !sawTask || !sawDelivery || h.count("shared_delivery_reviews") != 1 {
		t.Fatal("incremental refresh lost accepted work or its original delivery", delta)
	}
	if quietAgain := readContext(delta.NextCursor); quietAgain.Kind != "delta" || len(quietAgain.Items) != 0 {
		t.Fatal("unchanged final context replayed the delivery history", quietAgain)
	}
	for table, before := range counts {
		if h.count(table) != before {
			t.Fatalf("final context refresh repeated %s", table)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		reviews, err := views.SharedDeliveryReviews(t.Context(), tx.Q(), issue.IssueID, candidate.DeliveryID, "", 10)
		if err != nil {
			return err
		}
		if len(reviews.Items) != 1 || reviews.Items[0].ReviewID != reviewID || len(reviews.Items[0].Rounds) != 1 {
			t.Fatal("historical settlement repeated the whole-PR review", reviews.Items)
		}
		latest, err := store.LatestUpgrade(t.Context(), tx.Q())
		if err != nil {
			return err
		}
		if latest.View.UpgradeID != "later-platform-maintenance" || latest.View.SourceCommit == batch.View.SourceCommit {
			t.Fatal("settlement rewrote the independent current deployment", latest.View)
		}
		original, err := store.GetUpgrade(t.Context(), tx.Q(), batch.View.UpgradeID)
		if err == nil && !reflect.DeepEqual(original, batch) {
			t.Fatal("settlement changed the original installation facts", original)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
