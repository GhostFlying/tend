package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestEndToEnd_RunMessageHostedToolsAndRunHTTPContract(t *testing.T) {
	srv := newServer(t, time.Minute)
	controller := testkit.NewHarness()
	type nativeControl struct {
		thread *testkit.FakeThread
		turn   string
	}
	ready := make(chan nativeControl, 1)
	release := make(chan struct{})
	defer close(release)
	controller.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		if strings.Contains(in.Text, "Send supplemental context to this run") {
			ready <- nativeControl{th, turn}
			<-release
		}
		return "completed"
	}
	srv.startMainThread(controller)

	srv.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, protocol.OpRunnerHello, protocol.Target{Type: "runner"}, protocol.HelloPayload{Runtime: &protocol.RunnerRuntime{InstanceID: "message-runner", Generation: 1, Sequence: 1, Protocol: 1, ProtocolMin: 0, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRunMessages}}}, app.Hello{})
	task := srv.createTask()
	claimBody, _ := json.Marshal(protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: &protocol.RunnerRuntime{InstanceID: "message-runner", Generation: 1, Sequence: 2, Protocol: 1, ProtocolMin: 0, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRunMessages}}})
	claim := &protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunnerClaim, Target: protocol.Target{Type: "runner"}, Payload: claimBody}
	as := srv.claimDirectly(claim)
	if !as.RunMessages {
		t.Fatal("message capability not negotiated")
	}
	srv.exec(protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}, protocol.OpRunsReportStarted, protocol.Target{Type: "run", ID: as.RunID}, nil, app.ReportRunStarted{})
	srv.say("Send supplemental context to this run")
	var native nativeControl
	select {
	case native = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("hosted controller did not start")
	}
	result := native.thread.CallTool(t.Context(), native.turn, "send-message", "runs_send_message", map[string]any{"task_id": task, "run_id": as.RunID, "spec_version": as.SpecVersion, "generation": as.Generation, "expected_control_revision": srv.task(task).ControlRevision, "text": "supplemental context", "commit_seq": 1})
	if !result.Success {
		t.Fatal(result.Text)
	}
	text := result.Text
	var sent struct {
		Result struct {
			Outcome protocol.RunMessage `json:"outcome"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(text), &sent) != nil || sent.Result.Outcome.MessageID == "" || sent.Result.Outcome.Status != protocol.RunMessageQueued {
		t.Fatal(text)
	}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.url+"/runner/v1/messages?wait=0", nil)
	request.Header.Set(runnerapi.HeaderRun, as.RunID)
	request.Header.Set("Authorization", "Bearer "+as.RunToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var page protocol.RunMessagePage
	err = json.NewDecoder(response.Body).Decode(&page)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(page.Messages) != 1 || page.Messages[0].Source.Thread != native.thread.ID() || page.Messages[0].Source.Agent != "" || page.Messages[0].SourceMessageID == "" {
		t.Fatal(page, err)
	}
	binding := protocol.RunMessageDelivery{MessageID: sent.Result.Outcome.MessageID, Receiver: "original", ThreadID: "thread", TurnID: "turn"}
	for _, operation := range []string{protocol.OpRunsClaimMessage, protocol.OpRunsReportMessage} {
		var payload any = binding
		if operation == protocol.OpRunsReportMessage {
			payload = protocol.RunMessageReceipt{RunMessageDelivery: binding, Status: protocol.RunMessageAccepted}
		}
		body, _ := json.Marshal(payload)
		env := protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: operation, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: body}
		raw, _ := json.Marshal(env)
		request, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, srv.url+runnerapi.Path, bytes.NewReader(raw))
		request.Header.Set(runnerapi.HeaderRun, as.RunID)
		request.Header.Set("Authorization", "Bearer "+as.RunToken)
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Operation protocol.Operation `json:"operation"`
		}
		err = json.NewDecoder(response.Body).Decode(&result)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || result.Operation.Status != protocol.OpSucceeded {
			t.Fatal(result, err)
		}
	}
	result = native.thread.CallTool(t.Context(), native.turn, "read-messages", "runs_read_messages", map[string]any{"run_id": as.RunID})
	if !result.Success {
		t.Fatal(result.Text)
	}
	text = result.Text
	var read struct {
		Result protocol.RunMessagePage `json:"result"`
	}
	_ = json.Unmarshal([]byte(text), &read)
	if len(read.Result.Messages) != 1 || read.Result.Messages[0].Status != protocol.RunMessageAccepted || len(read.Result.Messages[0].History) != 3 {
		t.Fatal(text)
	}
	request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, srv.url+"/runner/v1/messages", nil)
	request.Header.Set(runnerapi.HeaderHost, "linux-1")
	request.Header.Set("Authorization", "Bearer "+hostToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("host identity read worker messages")
	}
}
