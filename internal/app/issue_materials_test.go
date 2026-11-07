package app

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func materialMessage(t *testing.T, h *harness, issue string, files ...string) string {
	t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Additional original evidence for this Issue", Attachments: files}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	var result struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	if op := h.associate(owner, result.MessageID, issue); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return result.MessageID
}
func TestIssueMaterialsManagerReadsBackgroundWithoutMessage(t *testing.T) {
	h, rt := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "original.json", "application/json", []byte(`{"retained":true}`))
	issue.Plan.Background = []protocol.SharedFact{{Kind: protocol.SharedFactUserConstraint, Text: "Read the saved original " + file.FileID, Source: file.FileID}}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Continue this Issue"}, rev(issue.Revision)), RequestIssueManager{})
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: rt, WorkspaceProtocol: 1}, nil), Claim{})
	var claim claimResult
	if err := json.Unmarshal(op.Result, &claim); err != nil {
		t.Fatal(err)
	}
	metadata, err := h.app.ManagerFile(t.Context(), runPrincipal(claim.Assignment.RunID, protocol.RoleIssueManager), file.FileID)
	if err != nil || metadata.SHA256 != file.SHA256 {
		t.Fatal("normal background original is unavailable", metadata, err)
	}
}
func TestIssueMaterialsFrozenDeliveryAndAcceptanceShareSavedOriginals(t *testing.T) {
	h, issue, source, candidate := deliveryFixtureOnHarness(t, newDeliverableHarness(t))
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	answerDeliveryForTest(t, h, duty, "approve", "")
	op := createDeliveryExecutionForTest(t, h, issue.IssueID, duty)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != created.TaskID {
		t.Fatal(impl)
	}
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	file := uploadChatFile(t, h, "installation.json", "application/json", []byte(`{"actual":"installed"}`))
	message := materialMessage(t, h, issue.IssueID, file.FileID)
	before := h.task(created.TaskID)
	spec := h.storedSpec(created.TaskID, before.SpecVersion)
	if len(spec.Inputs) != 0 {
		t.Fatal("fixture already has inputs")
	}
	frozen := protocol.OwnerTaskSpec{Goal: spec.Goal, Inputs: []protocol.OwnerTaskInput{{FileID: file.FileID}}}
	update, err := h.app.Execute(t.Context(), owner, h.env("issues.update_task", taskTarget(created.TaskID), protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &frozen}, rev(before.ControlRevision)), CanonicalOwnerTaskUpdate{})
	if err != nil || update.Error == nil || update.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("fixture scope is not frozen", update, err)
	}
	counts := map[string]int{}
	for _, table := range []string{"request_records", "events", "outbox", "plan_actions", "task_specs"} {
		counts[table] = h.count(table)
	}
	read := func(as protocol.Assignment) {
		t.Helper()
		ctx, err := h.app.ReadRunContext(t.Context(), runPrincipal(as.RunID, as.Role), protocol.IssueMaterialsQuery{Kind: "files"})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range ctx.Materials.Files {
			found = found || f.FileID == file.FileID
		}
		if !found {
			t.Fatal("original missing from index", ctx)
		}
		meta, body, err := h.app.OpenRunContextFile(t.Context(), runPrincipal(as.RunID, as.Role), file.FileID)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(raw) != `{"actual":"installed"}` || meta.SHA256 != file.SHA256 {
			t.Fatal(meta, err)
		}
	}
	read(impl)
	if _, body, err := h.app.OpenRunFile(t.Context(), runPrincipal(impl.RunID, impl.Role), file.FileID); err == nil {
		body.Close()
		t.Fatal("old file request unexpectedly changed")
	}
	for table, count := range counts {
		if h.count(table) != count {
			t.Fatal("read changed", table)
		}
	}
	messages, err := h.app.ReadRunContext(t.Context(), runPrincipal(impl.RunID, impl.Role), protocol.IssueMaterialsQuery{Kind: "messages", ID: message})
	if err != nil || len(messages.Materials.Messages) != 1 || messages.Materials.Messages[0].Body == "" {
		t.Fatal(messages, err)
	}
	result := testArtifact(h.task(created.TaskID).WorkspaceName)
	result.Report = &protocol.ResultReport{Body: "Review actual saved installation evidence"}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, result, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatal(accept)
	}
	h.runAs(accept, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	read(accept)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictInconclusive}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var decision string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT decision_id FROM decisions WHERE task_id=? AND status='open'`, created.TaskID).Scan(&decision)
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: decision}, protocol.AnswerDecisionPayload{Version: 1, Option: "retry acceptance"}, nil), AnswerDecision{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.Role != protocol.RoleAccept {
		t.Fatal(next)
	}
	h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	read(next)
	after := h.task(created.TaskID)
	if after.SpecVersion != before.SpecVersion || after.CurrentArtifactID == "" || !strings.Contains(spec.Goal, "approved") {
		t.Fatal(after)
	}
}

func TestIssueMaterialsSourcesFactsAndCapabilityHistory(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != task {
		t.Fatal(impl)
	}
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	file := uploadChatFile(t, h, "source.txt", "text/plain", []byte("unchanged original"))
	first := materialMessage(t, h, issue.IssueID, file.FileID)
	second := materialMessage(t, h, issue.IssueID, file.FileID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE conversation_message_controls SET withdrawn_at=?,revision=revision+1 WHERE message_id=?`, h.clock.Now().UnixMilli(), first); err != nil {
			return err
		}
		for i := range 3 {
			if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_context_facts(fact_id,work_item_id,scope,kind,body,source_message_id,attribution,superseded,needs_review,created_at) VALUES(?,?,'work_item','goal',?,?,'owner',?,?,?)`, h.ids.New(protocol.Prefix("fact")), issue.IssueID, []string{"historical", "pending", "current"}[i], second, i == 0, i == 1, h.clock.Now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := runPrincipal(impl.RunID, impl.Role)
	ctx, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "sources", Limit: 1})
	if err != nil || !ctx.Materials.More || len(ctx.Materials.Sources) != 1 {
		t.Fatal(ctx, err)
	}
	var seen []string
	after := ""
	for {
		page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "sources", After: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range page.Materials.Sources {
			seen = append(seen, source.MessageID)
			if source.MessageID == first && (source.Control == nil || source.Control.WithdrawnAt == 0 || source.Body == "") {
				t.Fatal("withdrawn original erased", source)
			}
			if source.MessageID == second && source.State != protocol.MessageQueued {
				t.Fatal("queued state was hidden", source)
			}
		}
		if !page.Materials.More {
			break
		}
		after = page.Materials.Next
	}
	if len(seen) < 2 {
		t.Fatal(seen)
	}
	facts, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "facts"})
	if err != nil || len(facts.Materials.Facts) != 3 {
		t.Fatal(facts, err)
	}
	var historical, pending bool
	for _, f := range facts.Materials.Facts {
		historical = historical || f.Superseded
		pending = pending || f.NeedsReview
	}
	if !historical || !pending {
		t.Fatal(facts)
	}
	putTaskCapability(t, h, "issue_context_fixture", "active", 0)
	putTaskCapability(t, h, "issue_context_fixture", "retired", 1)
	putTaskCapability(t, h, "issue_context_fixture", "active", 2)
	after = ""
	var revisions []int64
	for {
		page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "capabilities", ID: "issue_context_fixture", After: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if page.Materials.Capabilities == nil {
			t.Fatal(page)
		}
		for _, definition := range page.Materials.Capabilities.Items {
			revisions = append(revisions, definition.Revision)
		}
		if !page.Materials.More {
			break
		}
		after = page.Materials.Next
	}
	if len(revisions) != 3 || revisions[0] != 1 || revisions[2] != 3 {
		t.Fatal(revisions)
	}
	for _, kind := range []string{"agent_capabilities", "task_capabilities", "deployment"} {
		page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: kind})
		if err != nil {
			t.Fatal(kind, page, err)
		}
	}
	if _, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "deployment", ID: "foreign"}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("deployment selector ignored", err)
	}
	if _, err := h.app.ReadRunObservation(t.Context(), p); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("context broadened observation", err)
	}
}

func TestIssueMaterialsFilesScopeAndMissingReferences(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	other := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "own.json", "application/json", []byte("owner bytes"))
	missing := h.ids.New(protocol.PrefixFile)
	issue.Plan.Background = []protocol.SharedFact{{Kind: protocol.SharedFactUserConstraint, Text: "Saved " + file.FileID + "; historical unavailable reference " + missing, Source: file.FileID}}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	task := createCanonicalTaskForTest(t, h, issue)
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != task {
		t.Fatal(impl)
	}
	foreign := uploadChatFile(t, h, "foreign.json", "application/json", []byte("other Issue"))
	materialMessage(t, h, other.IssueID, foreign.FileID)
	p := runPrincipal(impl.RunID, impl.Role)
	page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "files"})
	if err != nil || len(page.Materials.Files) != 1 || page.Materials.Files[0].FileID != file.FileID {
		t.Fatal(page, err)
	}
	if _, err := h.app.ReadRunContextFile(t.Context(), p, foreign.FileID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("unrelated file became context", err)
	}
	if _, err := h.app.ReadRunContextFile(t.Context(), p, missing); err == nil {
		t.Fatal("missing reference became real metadata")
	}
	wrong := p
	wrong.Role = protocol.RoleAccept
	if _, err := h.app.ReadRunContext(t.Context(), wrong, protocol.IssueMaterialsQuery{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
}

func TestIssueMaterialsRetainsFrozenContextOriginalAfterBackgroundChanges(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "frozen.txt", "text/plain", []byte("original frozen context"))
	issue.Plan.Background = []protocol.SharedFact{{Kind: protocol.SharedFactUserConstraint, Text: "Original " + file.FileID, Source: file.FileID}}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	task := createCanonicalTaskForTest(t, h, issue)
	as, ok, _ := h.claim("linux-1")
	if !ok || as.TaskID != task || !strings.Contains(string(as.Spec), file.FileID) {
		t.Fatal("fixture did not freeze original context", as)
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Background = nil
	updated := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	if updated.Status != protocol.OpSucceeded {
		t.Fatal(updated)
	}
	p := runPrincipal(as.RunID, as.Role)
	page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "files"})
	if err != nil || len(page.Materials.Files) != 1 || page.Materials.Files[0].FileID != file.FileID {
		t.Fatal("frozen original disappeared from context", page, err)
	}
	metadata, body, err := h.app.OpenRunContextFile(t.Context(), p, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "original frozen context" || metadata.SHA256 != file.SHA256 {
		t.Fatal(metadata, err)
	}
}

func TestIssueMaterialsMarksTruncatedFileProvenance(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "shared.txt", "text/plain", []byte("same original"))
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for range 101 {
			id := h.ids.New(protocol.PrefixMessage)
			if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, Author: "owner", Body: "Saved source", CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
				return err
			}
			if err := tx.BindConversationAttachment(t.Context(), id, 0, file.FileID); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'owner',?)`, id, issue.IssueID, h.clock.Now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	metadata, err := h.app.ReadIssueMaterialFile(t.Context(), issue.IssueID, file.FileID)
	if err != nil || !metadata.SourcesMore || len(metadata.Sources) != 100 {
		t.Fatal("partial provenance presented as complete", metadata, err)
	}
	first, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "sources", Limit: 100})
	if err != nil || !first.More || len(first.Sources) != 100 {
		t.Fatal(first, err)
	}
	last, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "sources", After: first.Next, Limit: 100})
	if err != nil || last.More || len(last.Sources) != 1 {
		t.Fatal(last, err)
	}
}

func TestIssueMaterialsAcceptanceTaskSpecKeepsOnlyPublicContext(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "public.txt", "text/plain", []byte("public Issue original"))
	materialMessage(t, h, issue.IssueID, file.FileID)
	task := createCanonicalTaskForTest(t, h, issue)
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != task {
		t.Fatal(impl)
	}
	read := func(p protocol.Principal, id string, private bool) {
		t.Helper()
		page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "tasks", ID: id})
		if err != nil || len(page.Materials.Tasks) != 1 {
			t.Fatal(page, err)
		}
		raw := string(page.Materials.Tasks[0].Spec)
		if strings.Contains(raw, "PRIVATE_OWNER_IMPLEMENTATION_REASONING") != private {
			t.Fatal("incorrect implementation-only projection", raw)
		}
		var spec map[string]json.RawMessage
		if err := json.Unmarshal(page.Materials.Tasks[0].Spec, &spec); err != nil {
			t.Fatal(err)
		}
		if !private && spec["implementation_context"] != nil {
			t.Fatal("private field remains present", raw)
		}
		if len(spec["goal"]) == 0 || len(spec["acceptance"]) == 0 {
			t.Fatal("public Task context was hidden", raw)
		}
	}
	read(runPrincipal(impl.RunID, impl.Role), task, true)
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	artifact := testArtifact(h.task(task).WorkspaceName)
	artifact.Report = &protocol.ResultReport{Body: "PUBLIC_SUBMITTED_AUTHOR_REPORT"}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok || accept.Role != protocol.RoleAccept || strings.Contains(string(accept.Spec), "PRIVATE_OWNER_IMPLEMENTATION_REASONING") {
		t.Fatal(accept)
	}
	if strings.Contains(string(accept.Artifact), "PUBLIC_SUBMITTED_AUTHOR_REPORT") {
		t.Fatal("optional author narrative was automatically injected", string(accept.Artifact))
	}
	for _, kind := range []string{"tasks", "artifacts"} {
		page, err := h.app.ReadRunContext(t.Context(), runPrincipal(accept.RunID, accept.Role), protocol.IssueMaterialsQuery{Kind: kind})
		raw, marshalErr := json.Marshal(page)
		if err != nil || marshalErr != nil || !strings.Contains(string(raw), "PUBLIC_SUBMITTED_AUTHOR_REPORT") {
			t.Fatal("submitted public report cannot be deliberately read", page, err, marshalErr)
		}
	}
	read(runPrincipal(accept.RunID, accept.Role), task, false)
	read(runPrincipal(accept.RunID, accept.Role), "", false)
	if _, err := h.app.ReadRunContextFile(t.Context(), runPrincipal(accept.RunID, accept.Role), file.FileID); err != nil {
		t.Fatal("acceptance lost ordinary shared originals", err)
	}
	for _, root := range []string{"", issue.IssueID} {
		page, err := h.app.ReadScopedIssueMaterials(t.Context(), root, issue.IssueID, protocol.IssueMaterialsQuery{Kind: "tasks", ID: task})
		if err != nil || len(page.Tasks) != 1 || !strings.Contains(string(page.Tasks[0].Spec), "PRIVATE_OWNER_IMPLEMENTATION_REASONING") {
			t.Fatal("owner/manager projection changed", page, err)
		}
	}
}

func TestIssueMaterialsHistoricalTaskContextSurvivesSuccessor(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "historical.txt", "text/plain", []byte("historical original"))
	issue.Plan.Background = []protocol.SharedFact{{Kind: protocol.SharedFactInput, Text: "Original evidence", Source: file.FileID}}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	task := createCanonicalTaskForTest(t, h, issue)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Background = nil
	updated := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	if updated.Status != protocol.OpSucceeded {
		t.Fatal(updated)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	var revised protocol.OwnerTaskSpec
	raw, err := json.Marshal(h.storedSpec(task, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &revised); err != nil {
		t.Fatal(err)
	}
	revised.Goal += "; verify the current implementation"
	if op := h.exec(owner, h.env("issues.update_task", taskTarget(task), protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &revised}, rev(h.task(task).ControlRevision)), CanonicalOwnerTaskUpdate{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != task || impl.SpecVersion != 2 || strings.Contains(string(impl.Spec), file.FileID) {
		t.Fatal("current spec must not retain the historical reference", impl)
	}
	if _, err := h.app.ReadIssueMaterialFile(t.Context(), issue.IssueID, file.FileID); err != nil {
		t.Fatal("same original missing from Issue resolver", err)
	}
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatal(accept)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	nextTask := createCanonicalTaskForTest(t, h, issue)
	next, ok, _ := h.claim("linux-1")
	if !ok || next.TaskID != nextTask || strings.Contains(string(next.Spec), file.FileID) {
		t.Fatal("successor must not already contain original reference", next)
	}
	p := runPrincipal(next.RunID, next.Role)
	page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "files"})
	if err != nil || len(page.Materials.Files) != 1 || page.Materials.Files[0].FileID != file.FileID {
		t.Fatal("historical original missing from successor index", page, err)
	}
	metadata, body, err := h.app.OpenRunContextFile(t.Context(), p, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	bytes, err := io.ReadAll(body)
	if err != nil || string(bytes) != "historical original" || metadata.SHA256 != file.SHA256 {
		t.Fatal(metadata, err)
	}
	found := false
	for _, source := range metadata.Sources {
		found = found || source.Kind == "task_context" && source.TaskID == task && source.ContentRevision == 1
	}
	if !found {
		t.Fatal("historical spec provenance missing", metadata)
	}
}

func TestIssueMaterialsManagerReadsHistoricalTaskContext(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "historical.txt", "text/plain", []byte("retained historical original"))
	issue.Plan.Background = []protocol.SharedFact{{Kind: protocol.SharedFactInput, Text: "Original evidence", Source: file.FileID}}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	task := createCanonicalTaskForTest(t, h, issue)
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(task), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(task).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Background = nil
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Continue from saved materials"}, rev(issue.Revision)), RequestIssueManager{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{})
	var claimed claimResult
	if err := json.Unmarshal(op.Result, &claimed); err != nil || claimed.Assignment.Role != protocol.RoleIssueManager {
		t.Fatal(op, err)
	}
	p := runPrincipal(claimed.Assignment.RunID, protocol.RoleIssueManager)
	metadata, err := h.app.ManagerFile(t.Context(), p, file.FileID)
	if err != nil || metadata.SHA256 != file.SHA256 {
		t.Fatal("manager cannot recover historical context original", metadata, err)
	}
	page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "tasks", ID: task})
	if err != nil || len(page.Materials.Tasks) != 1 || !strings.Contains(string(page.Materials.Tasks[0].Spec), "PRIVATE_OWNER_IMPLEMENTATION_REASONING") {
		t.Fatal("manager original spec projection changed", page, err)
	}
}

func TestIssueMaterialsLegacyDefaultOverviewAndDeliveries(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	p := runPrincipal(as.RunID, as.Role)
	before := h.count("events")
	out, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{})
	if err != nil {
		t.Fatal("default reader rejects retained legacy context", out, err)
	}
	raw, err := json.Marshal(out.Materials)
	var fields map[string]json.RawMessage
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var legacy []protocol.WorkItem
	if err := json.Unmarshal(fields["legacy_contexts"], &legacy); err != nil || len(legacy) != 1 || len(out.Materials.Issues) != 0 {
		t.Fatal("legacy context was dropped or relabeled canonical", out, err)
	}
	ownerPage, err := h.app.ReadIssueMaterials(t.Context(), legacy[0].WorkItemID, protocol.IssueMaterialsQuery{})
	if err != nil {
		t.Fatal(ownerPage, err)
	}
	page, err := h.app.ReadRunContext(t.Context(), p, protocol.IssueMaterialsQuery{Kind: "deliveries"})
	if err != nil || len(page.Materials.Deliveries) != 0 {
		t.Fatal("legacy scope created canonical delivery authority", page, err)
	}
	if h.count("events") != before {
		t.Fatal("legacy reading wrote new facts")
	}
}
