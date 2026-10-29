package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDirectCLIUploadsOriginalInjectsEvidenceAndMessagesExactRun(t *testing.T) {
	e := newEnv(t)
	e.configure()
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Home: t.TempDir(), ConfigDir: filepath.Join(t.TempDir(), "config"), IDs: e.ids, Stdin: bytes.NewReader(nil), Stdout: &stdout, Stderr: &stderr, HTTP: http.DefaultClient, Getenv: func(string) string { return "" }}
	command := func(args ...string) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := cli.Main(t.Context(), env, args); code != 0 {
			t.Fatalf("%v: %d %s %s", args, code, stdout.String(), stderr.String())
		}
	}
	command("login", "--server", e.url)
	original := []byte("original file\x00\xff")
	filePath := filepath.Join(t.TempDir(), "original.bin")
	if err := os.WriteFile(filePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	command("attachment", "upload", filePath)
	var uploaded protocol.Operation
	if err := json.Unmarshal(stdout.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(uploaded.Result, &file); err != nil {
		t.Fatal(err)
	}
	injection := protocol.InjectSourcePayload{Text: "external original", Attachments: []string{file.FileID}, Metadata: protocol.ManualSourceMetadata{URL: "https://example.com/evidence"}}
	raw, _ := json.Marshal(injection)
	command("source", "inject", "main", "--data", string(raw))
	var response protocol.APIResponse
	_ = json.Unmarshal(stdout.Bytes(), &response)
	var source protocol.ManualSource
	_ = json.Unmarshal(response.Operation.Result, &source)
	command("source", "show", source.MessageID)
	var read protocol.ManualSource
	if err := json.Unmarshal(stdout.Bytes(), &read); err != nil || read.Text != injection.Text || read.SuppliedBy.Subject != ownerLogin || read.Metadata.AtMillis != nil || read.Metadata.Author != "" {
		t.Fatal(stdout.String(), err)
	}
	command("attachment", "download", file.FileID, "--out", filepath.Join(t.TempDir(), "readback.bin"))
	_, reader, err := e.app.OpenConversationAttachment(t.Context(), file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data := make([]byte, len(original))
	_, err = reader.Read(data)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("original changed", err)
	}
	if e.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatal("source injection created work")
	}
	task := e.task("original active worker")
	rt := &protocol.RunnerRuntime{InstanceID: "message-runner", Generation: 1, Sequence: 1, Protocol: 1, ProtocolMin: 0, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRunMessages, protocol.CapabilityWorkerQuestions, protocol.CapabilityRunnerHeartbeat}}
	runner := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "host-a"}
	issueWorkerCommand(t, e, runner, protocol.Target{Type: "host", ID: "host-a"}, protocol.HelloPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: rt}, app.Hello{})
	claimed := issueWorkerCommand(t, e, runner, protocol.Target{Type: "host", ID: "host-a"}, protocol.ClaimPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: rt}, app.Claim{})
	var result struct {
		Assignment protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(claimed.Result, &result); err != nil || result.Assignment.TaskID != task || !result.Assignment.RunMessages {
		t.Fatal(string(claimed.Result), err)
	}
	as := result.Assignment
	worker := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
	issueWorkerCommand(t, e, worker, protocol.Target{Type: "run", ID: as.RunID}, nil, app.ReportRunStarted{})
	var revision int64
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		task, err := store.GetTask(t.Context(), tx.Q(), as.TaskID)
		revision = task.ControlRevision
		return err
	}); err != nil {
		t.Fatal(err)
	}
	text := protocol.SendRunMessagePayload{TaskID: as.TaskID, SpecVersion: as.SpecVersion, Generation: as.Generation, Text: "Additional owner evidence"}
	raw, _ = json.Marshal(text)
	request := e.ids.New(protocol.PrefixRequest)
	command("run", "message", as.RunID, "--revision", strconv.FormatInt(revision, 10), "--request-id", request, "--data", string(raw))
	_ = json.Unmarshal(stdout.Bytes(), &response)
	var supplement protocol.RunMessage
	_ = json.Unmarshal(response.Operation.Result, &supplement)
	if supplement.Source.Kind != protocol.PrincipalOwner || supplement.Source.Subject != ownerLogin || supplement.SourceMessageID != "" {
		t.Fatal(supplement)
	}
	command("run", "message", as.RunID, "--revision", strconv.FormatInt(revision, 10), "--request-id", request, "--data", string(raw))
	receipt := issueWorkerCommand(t, e, worker, protocol.Target{Type: "run", ID: as.RunID}, protocol.RunMessageDelivery{MessageID: supplement.MessageID, Receiver: "original-worker", ThreadID: "native-thread", TurnID: "native-turn"}, app.ClaimRunMessage{})
	_ = json.Unmarshal(receipt.Result, &supplement)
	issueWorkerCommand(t, e, worker, protocol.Target{Type: "run", ID: as.RunID}, protocol.RunMessageReceipt{RunMessageDelivery: supplement.RunMessageDelivery, Status: protocol.RunMessageAccepted}, app.ReportRunMessage{})
	beforeRequests := e.count(`SELECT COUNT(*) FROM request_records`)
	beforeOutbox := e.count(`SELECT COUNT(*) FROM outbox`)
	command("run", "native-entry", as.RunID)
	var entry protocol.NativeEntry
	if err := json.Unmarshal(stdout.Bytes(), &entry); err != nil || entry.RunID != as.RunID || entry.TaskID != task || entry.SessionRecorded || entry.ControllerObservation != "unknown" {
		t.Fatal(entry, err)
	}
	command("run", "native-entry", as.RunID, "--provider", "cove")
	if err := json.Unmarshal(stdout.Bytes(), &entry); err != nil || entry.AdapterImplemented || entry.Reason != "adapter_not_implemented" {
		t.Fatal(entry, err)
	}
	if e.count(`SELECT COUNT(*) FROM request_records`) != beforeRequests || e.count(`SELECT COUNT(*) FROM outbox`) != beforeOutbox {
		t.Fatal("viewing native entry sent a control effect")
	}
	command("run", "messages", as.RunID)
	var page protocol.RunMessagePage
	if err := json.Unmarshal(stdout.Bytes(), &page); err != nil || len(page.Messages) != 1 || page.Messages[0].Status != protocol.RunMessageAccepted {
		t.Fatal(stdout.String(), err)
	}
}

func TestDirectCLIConsultationAndOwnerPromotionUseSelectedOriginal(t *testing.T) {
	e := newEnv(t)
	e.configure()
	principal := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}
	zero := int64(0)
	put := e.envelope("categories.put", protocol.Target{Type: "category", ID: "planner"}, &zero, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"})
	if op, err := e.app.Execute(t.Context(), principal, &put, app.PutAgentCategory{}); err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	config := hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "adviser", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "HOSTED_EXECUTABLE", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "adviser", Revision: 1, DisplayName: "Adviser", RuntimeID: "runtime", Categories: []string{"planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation}, Model: "model", Enabled: true}}}}
	prepared, err := hostedconfig.Prepare(config)
	if err != nil {
		t.Fatal(err)
	}
	admit := e.envelope("hosted.configure", protocol.Target{Type: "hosted_configuration"}, &zero, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()})
	if op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, &admit, app.AcceptHostedConfiguration{Prepared: prepared}); err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	e.app = app.New(e.store, e.ids, e.clock.Now, app.WithHostedConfiguration(prepared))
	e.server.app = e.app
	e.server.cfg.HostedMainAgent = true
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Home: t.TempDir(), ConfigDir: filepath.Join(t.TempDir(), "config"), Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(""), IDs: e.ids, HTTP: http.DefaultClient, Getenv: func(string) string { return "" }}
	command := func(args ...string) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := cli.Main(t.Context(), env, args); code != 0 {
			t.Fatal(args, stdout.String(), stderr.String())
		}
	}
	command("login", "--server", e.url)
	command("conversation", "consult", "--data", `{"agent_ref":"hosted/adviser","title":"Discuss a question"}`)
	var response protocol.APIResponse
	var consultation protocol.AgentConversation
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(response.Operation.Result, &consultation); err != nil {
		t.Fatal(err)
	}
	if consultation.Kind != "consultation" || consultation.AgentRef != "hosted/adviser" {
		t.Fatal(consultation)
	}
	command("conversation", "list")
	if !strings.Contains(stdout.String(), consultation.ID) || strings.Contains(stdout.String(), "HOSTED_EXECUTABLE") {
		t.Fatal(stdout.String())
	}
	command("conversation", "send", consultation.ID, "--data", `{"body":"How does this work?"}`)
	if e.count(`SELECT COUNT(*) FROM tasks`) != 0 || e.count(`SELECT COUNT(*) FROM issue_controls`) != 0 {
		t.Fatal("ordinary chat created work")
	}
	command("conversation", "send", consultation.ID, "--data", `{"body":"Please execute the report discussed here"}`)
	_ = json.Unmarshal(stdout.Bytes(), &response)
	var intent struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(response.Operation.Result, &intent)
	payload := protocol.PromoteConsultationPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: intent.MessageID, ExpectedSourceRevision: func() *int64 { v := int64(1); return &v }()}, Plan: protocol.WorkItemPlan{Title: "Prepare investigation report", Project: "default", Intent: "Return independently reviewed evidence"}, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "hosted/adviser"}}
	raw, _ := json.Marshal(payload)
	request := e.ids.New(protocol.PrefixRequest)
	command("conversation", "promote", consultation.ID, "--data", string(raw), "--request-id", request)
	first := stdout.String()
	command("conversation", "promote", consultation.ID, "--data", string(raw), "--request-id", request)
	if stdout.String() != first || e.count(`SELECT COUNT(*) FROM issue_controls`) != 1 || e.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatal("owner promotion replay changed or bypassed manager", stdout.String())
	}
	command("conversation", "show", consultation.ID)
	if !strings.Contains(stdout.String(), "Please execute") {
		t.Fatal("source conversation lost", stdout.String())
	}
}

func TestDirectConversationWritesRespectArchiveBodyAdmission(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	if _, err := e.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: e.ids.New(protocol.PrefixRequest)}, true); err != nil {
		t.Fatal(err)
	}
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"conversation.submit_message", "conversation.inject_source", "conversation.consult", "conversation.promote", protocol.OpRunsSendMessage} {
		t.Run(operation, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"operation": operation})
			body := &archiveSlowBody{reader: bytes.NewReader(raw), entered: make(chan struct{}), release: make(chan struct{})}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/ops", body)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set(deploymentHeader, deployment)
			response := httptest.NewRecorder()
			e.server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || body.reads.Load() != 0 {
				t.Fatal("prepared server read a new chat/source/promotion body", response.Code, body.reads.Load())
			}
		})
	}
}
