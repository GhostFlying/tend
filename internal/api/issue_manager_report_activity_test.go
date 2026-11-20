package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueManagerReportActivityHTTP(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	otherIssue := createOwnerIssueHTTP(t, e, token, "")
	issue := admitOwnerIssueHTTP(t, e, token, createOwnerIssueHTTP(t, e, token, ""))
	run := claimIssueReportManager(t, e)
	if run.Manager == nil || run.Manager.IssueID != issue.IssueID {
		t.Fatal(run)
	}
	principal := protocol.Principal{Kind: protocol.PrincipalRun, Subject: run.RunID, Role: run.Role}
	issueWorkerCommand(t, e, principal, protocol.Target{Type: "run", ID: run.RunID}, nil, app.ReportRunStarted{})
	issue = issueDetailHTTP(t, e, token, issue.IssueID).Issue
	thread, conversation, turnID := startIssueReportTurn(t, e, run)
	principal.Thread, principal.ConversationID = thread, conversation
	target := protocol.Target{Type: "issue", ID: issue.IssueID}
	path := "/api/v1/issues/" + issue.IssueID
	activity := func(path, query string) protocol.IssueActivityPage {
		t.Helper()
		var out protocol.IssueActivityPage
		if code := e.do("GET", path+"/activity?view=readable"+query, token, nil, &out); code != http.StatusOK {
			t.Fatalf("activity HTTP status %d", code)
		}
		return out
	}
	draft := retainIssueReportDraftFixture(t, e, run)
	before := activity(path, "")
	for _, entry := range before.Items {
		if entry.ArtifactID != "" || entry.Kind == "run.started" || entry.Kind == "action.dispatched" {
			t.Fatalf("internal activity became a discussion entry: %+v", entry)
		}
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewGray(image.Rect(0, 0, 64, 48))); err != nil {
		t.Fatal(err)
	}
	originals := [][]byte{imageBytes.Bytes(), []byte("# Original report notes\n\nThe layout fits a narrow screen.\n")}
	var files []protocol.ArtifactFile
	for i, name := range []string{"layout.png", "notes.md"} {
		op, err := e.app.UploadDeliverableFile(t.Context(), principal, e.ids.New(protocol.PrefixRequest), name, bytes.NewReader(originals[i]))
		var file protocol.ArtifactFile
		if err != nil || op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &file) != nil || file.FileID == "" {
			t.Fatal(op, err)
		}
		files = append(files, file)
	}
	const reportBody = "The manager's formal report is ready to read."
	payload := protocol.UpdateIssueDeliveryPayload{Action: "submit_result", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: reportBody}, Files: files}, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Submit the checked outcome and originals"}}
	envelope := e.envelope("issues.update_delivery", target, &issue.Revision, payload)
	command := app.HostedCoordinationCommand{Command: app.UpdateIssueDelivery{}, ExecutionID: turnID, EffectSeq: 1}
	op, err := e.app.Execute(t.Context(), principal, &envelope, command)
	var receipt struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err != nil || op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &receipt) != nil || receipt.ArtifactID == "" {
		t.Fatal(op, err)
	}
	if op, err := e.app.Execute(t.Context(), principal, &envelope, command); err != nil || op.Error == nil || op.Error.Code != protocol.CodeDuplicateEffect {
		t.Fatal("expected duplicate-effect receipt", op, err)
	}
	assertReport := func(page protocol.IssueActivityPage) protocol.IssueActivityEntry {
		t.Helper()
		var result protocol.IssueActivityEntry
		for _, entry := range page.Items {
			if entry.ArtifactID == draft {
				t.Fatalf("draft entered the discussion: %+v", entry)
			}
			if entry.ArtifactID == receipt.ArtifactID {
				if result.ID != "" || entry.ID != "result:"+receipt.ArtifactID || entry.Kind != "issue.result_submitted" || entry.TaskID != run.TaskID || entry.Block != nil {
					t.Fatalf("invalid or repeated result entry: %+v", entry)
				}
				result = entry
			}
		}
		if result.ID == "" {
			t.Fatal("formal result missing from activity", page)
		}
		return result
	}
	if active := issueDetailHTTP(t, e, token, issue.IssueID); active.State != protocol.IssueActive || active.Manager == nil || active.Manager.State != protocol.TaskRunning {
		t.Fatalf("the manager must still be running: %+v", active.Manager)
	}
	result := assertReport(activity(path, ""))
	if delta := assertReport(activity(path, fmt.Sprintf("&since=%d", before.EventSeq))); delta.Position != result.Position {
		t.Fatal("the live result moved", delta, result)
	}
	if earlier := activity(path, fmt.Sprintf("&before=%d", result.Position)); slices.ContainsFunc(earlier.Items, func(entry protocol.IssueActivityEntry) bool { return entry.ID == result.ID }) {
		t.Fatal("the exclusive history cursor repeated the result", earlier)
	}
	if other := activity("/api/v1/issues/"+otherIssue.IssueID, ""); slices.ContainsFunc(other.Items, func(entry protocol.IssueActivityEntry) bool { return entry.ArtifactID == receipt.ArtifactID }) {
		t.Fatal("the formal report escaped its owning Issue", other)
	}

	readResultAndFiles := func() {
		t.Helper()
		var results protocol.IssueResultPage
		if code := e.do("GET", path+"/results", token, nil, &results); code != http.StatusOK || len(results.Items) != 1 || results.Items[0].Artifact.ArtifactID != receipt.ArtifactID {
			t.Fatal(code, results)
		}
		var report protocol.ArtifactPayload
		if err := json.Unmarshal(results.Items[0].Artifact.Ref, &report); err != nil || report.Report == nil || report.Report.Body != reportBody || !slices.Equal(report.Files, files) {
			t.Fatal(report, err)
		}
		for i, file := range files {
			filePath := "/api/v1/files/" + file.FileID
			if code := e.do("GET", filePath+"/download-url", "", nil, nil); code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated file grant: %d", code)
			}
			var grant protocol.DownloadURLResponse
			if code := e.do("GET", filePath+"/download-url", token, nil, &grant); code != http.StatusOK || grant.URL == "" {
				t.Fatal(code, grant)
			}
			res, err := http.Get(e.url + grant.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusOK || err != nil || !bytes.Equal(body, originals[i]) {
				t.Fatalf("original attachment %q: status %d, err %v", file.Name, res.StatusCode, err)
			}
		}
	}
	readResultAndFiles()
	issue = issueDetailHTTP(t, e, token, issue.IssueID).Issue
	control := protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Settle the submitted report"}
	report := e.envelope("issues.update_delivery", target, &issue.Revision, protocol.UpdateIssueDeliveryPayload{Action: "report", TaskID: run.TaskID, IssueControlPayload: control})
	command.EffectSeq++
	if op, err := e.app.Execute(t.Context(), principal, &report, command); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	issue = issueDetailHTTP(t, e, token, issue.IssueID).Issue
	closeIssue := e.envelope("issues.close", target, &issue.Revision, protocol.IssueEndpointPayload{
		IssueControlPayload: control,
		Closure: &protocol.IssueClosure{Kind: "completed", Reason: "Report and original files delivered", Evidence: []protocol.IssueEvidence{{
			Kind: protocol.WorkItemEvidenceResult, Reference: receipt.ArtifactID, Summary: "Checked report with originals",
		}}},
	})
	command.Command = app.CanonicalOwnerIssueCommand{Action: "close"}
	command.EffectSeq++
	if op, err := e.app.Execute(t.Context(), principal, &closeIssue, command); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("close operation: %+v %v (%v)", op, op.Error, err)
	}
	if closed := issueDetailHTTP(t, e, token, issue.IssueID); closed.State != protocol.IssueClosed {
		t.Fatal(closed)
	}
	if historical := assertReport(activity(path, "")); historical.Position != result.Position {
		t.Fatal("closing moved the submitted report", historical, result)
	}
	readResultAndFiles()
}

func claimIssueReportManager(t *testing.T, e *env) protocol.Assignment {
	t.Helper()
	definition, err := agentconfig.Validate("host-a", agentconfig.Config{FormatVersion: 1,
		Runtimes: []agentconfig.RuntimeBinding{{ID: "manager", Revision: 1, Kind: agentconfig.AppServerWorker, ExecutableRef: "codex", DeclaredPurposes: []agentconfig.Purpose{agentconfig.IssueManager}, Enabled: true}},
		Agents:   []agentconfig.AgentDefinition{{LocalID: "manager", Revision: 1, DisplayName: "Fixture manager", RuntimeID: "manager", Categories: []string{"fixture-execution"}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: "fixture-model", Enabled: true}},
	}, agentconfig.SourceReference{Kind: agentconfig.Inline, Reference: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	projection := definition.Public()
	projection.FormatVersion = 2
	projection.Runtimes[0].CLI = "codex"
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	runner := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "host-a"}
	target := protocol.Target{Type: "host", ID: runner.Subject}
	runtime := &protocol.RunnerRuntime{InstanceID: "manager-fixture", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol,
		ProtocolMin: protocol.MinimumRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, JournalVersion: 13,
		Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityAgentInventory, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager, protocol.CapabilityIssueOwner},
	}
	hello := protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1, Worker: protocol.WorkerAppServer}
	issueWorkerCommand(t, e, runner, target, hello, app.Hello{})
	const snapshot = "manager-fixture-snapshot"
	register := app.RegisterAgentInventory{}
	envelope := e.envelope(register.Operation(), target, new(int64), protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: 1, SnapshotID: snapshot, PublicSHA256: hash, Projection: projection})
	if op, err := e.app.Execute(t.Context(), runner, &envelope, register); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	agent := projection.Agents[0]
	runtime.AgentExecutors = []protocol.AgentExecutor{{Available: true, Pin: protocol.RunExecutionPin{Purpose: "issue_manager", Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: 1, SnapshotID: snapshot, PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel}, Worker: protocol.WorkerAppServer}}}
	runtime.Sequence++
	issueWorkerCommand(t, e, runner, target, hello, app.Hello{})
	return issueClaimWorker(t, e)
}

func startIssueReportTurn(t *testing.T, e *env, run protocol.Assignment) (string, string, string) {
	t.Helper()
	conversation := run.Manager.ConversationID
	delivery, _, ok, err := e.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal("manager turn was not admitted", ok, err)
	}
	var payload app.HandleMessage
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	selected, named, err := e.app.NamedHostedSelection(t.Context(), conversation)
	if err != nil || !named {
		t.Fatal(selected, named, err)
	}
	turn := store.MainTurn{RequestID: delivery.RequestID, MessageID: payload.MessageID, ThreadID: "native-issue-report", Status: store.TurnStarting, Hosted: &selected, Model: selected.Definition.Model, Effort: selected.Definition.Effort}
	if err := e.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.TurnID = store.TurnStarted, "turn-issue-report"
	if err := e.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	return turn.ThreadID, conversation, turn.RequestID
}

func retainIssueReportDraftFixture(t *testing.T, e *env, run protocol.Assignment) string {
	t.Helper()
	id := e.ids.New(protocol.PrefixArtifact)
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		now := e.clock.Now().UnixMilli()
		if err := tx.InsertArtifact(t.Context(), id, run.TaskID, run.RunID, "result", `{"report":{"body":"Internal draft"}}`, "draft", now); err != nil {
			return err
		}
		for _, kind := range []string{"artifact.submitted", "issue.result_submitted"} {
			if _, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: e.ids.New(protocol.PrefixEvent), Source: "test", ObjectType: "artifact", ObjectID: id, Kind: kind, BodyJSON: "{}", CreatedAt: now}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}
