package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

var messageAgent = protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "control-client", Thread: "control-session"}

func messageRun(t *testing.T) (*harness, protocol.Assignment) {
	h := newHarness(t)
	runtime := testRuntime("message-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityRunMessages)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.createTask(owner, createTaskPayload{Title: "message check", Spec: testSpec})
	as, ok := h.claimInputRun(runtime)
	if !ok || !as.RunMessages {
		t.Fatal("message assignment not negotiated", as)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, as
}

func sendMessageEnv(h *harness, as protocol.Assignment, text string) *protocol.Envelope {
	return h.env(protocol.OpRunsSendMessage, runTarget(as.RunID), protocol.SendRunMessagePayload{TaskID: as.TaskID, SpecVersion: as.SpecVersion, Generation: as.Generation, Text: text, SourceMessageID: queueMessage(h.t, h, text), CommitSeq: 1}, rev(h.task(as.TaskID).ControlRevision))
}

func messageResult(t *testing.T, op protocol.Operation) protocol.RunMessage {
	t.Helper()
	var m protocol.RunMessage
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &m) != nil {
		t.Fatalf("message operation: %+v", op)
	}
	return m
}

func sendMessage(t *testing.T, h *harness, as protocol.Assignment, text string) protocol.RunMessage {
	t.Helper()
	return messageResult(t, h.exec(messageAgent, sendMessageEnv(h, as, text), SendRunMessage{}))
}

func claimMessage(t *testing.T, h *harness, as protocol.Assignment, m protocol.RunMessage) protocol.RunMessage {
	t.Helper()
	return messageResult(t, h.runAs(as, protocol.OpRunsClaimMessage, protocol.RunMessageDelivery{MessageID: m.MessageID, Receiver: "receiver-1", ThreadID: "native-thread", TurnID: "native-turn"}, ClaimRunMessage{}))
}

func readMessages(t *testing.T, h *harness, as protocol.Assignment) protocol.RunMessagePage {
	t.Helper()
	p, err := h.app.RunMessages(t.Context(), messageAgent, as.RunID, 0, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunMessagesFreezeContractAndDeduplicateRequests(t *testing.T) {
	h, as := messageRun(t)
	before := h.task(as.TaskID)
	env := sendMessageEnv(h, as, "Inspect the retained failing example.")
	first := h.exec(messageAgent, env, SendRunMessage{})
	m := messageResult(t, first)
	if second := h.exec(messageAgent, env, SendRunMessage{}); string(first.Result) != string(second.Result) {
		t.Fatal("request replay changed identity")
	}
	if m.Status != protocol.RunMessageQueued || m.Sequence != 1 || m.RunID != as.RunID || m.ActionID != as.ActionID || m.SpecVersion != as.SpecVersion || m.Generation != as.Generation || m.Source != messageAgent || m.RequestID != env.RequestID || len(m.History) != 1 {
		t.Fatalf("scope or audit: %+v", m)
	}
	if !reflect.DeepEqual(before, h.task(as.TaskID)) {
		t.Fatal("supplement changed task contract or acceptance")
	}
	changed := *env
	changed.Payload = json.RawMessage(`{"text":"different"}`)
	if _, err := h.app.Execute(t.Context(), messageAgent, &changed, SendRunMessage{}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal(err)
	}
	if len(readMessages(t, h, as).Messages) != 1 {
		t.Fatal("duplicate message")
	}
}

func TestRunMessagesOrderUnknownAndLateReceipt(t *testing.T) {
	h, as := messageRun(t)
	a := sendMessage(t, h, as, "first")
	b := sendMessage(t, h, as, "second")
	if op := h.runAs(as, protocol.OpRunsClaimMessage, protocol.RunMessageDelivery{MessageID: b.MessageID, Receiver: "r"}, ClaimRunMessage{}); op.Status != protocol.OpFailed {
		t.Fatal("out of order claim accepted")
	}
	a = claimMessage(t, h, as, a)
	unknown := protocol.RunMessageReceipt{RunMessageDelivery: a.RunMessageDelivery, Status: protocol.RunMessageUnknown, Reason: "native_outcome_unknown"}
	messageResult(t, h.runAs(as, protocol.OpRunsReportMessage, unknown, ReportRunMessage{}))
	if op := h.runAs(as, protocol.OpRunsClaimMessage, protocol.RunMessageDelivery{MessageID: a.MessageID, Receiver: "new"}, ClaimRunMessage{}); op.Status != protocol.OpFailed {
		t.Fatal("unknown resent")
	}
	if op := h.runAs(as, protocol.OpRunsClaimMessage, protocol.RunMessageDelivery{MessageID: b.MessageID, Receiver: "new"}, ClaimRunMessage{}); op.Status != protocol.OpFailed {
		t.Fatal("unknown bypassed")
	}
	unknown.Status, unknown.Reason = protocol.RunMessageAccepted, ""
	accepted := messageResult(t, h.runAs(as, protocol.OpRunsReportMessage, unknown, ReportRunMessage{}))
	if len(accepted.History) != 4 || accepted.Status != protocol.RunMessageAccepted {
		t.Fatal(accepted)
	}
	if repeated := messageResult(t, h.runAs(as, protocol.OpRunsReportMessage, unknown, ReportRunMessage{})); len(repeated.History) != 4 {
		t.Fatal("receipt duplicated history")
	}
	if got := claimMessage(t, h, as, b); got.Status != protocol.RunMessageDispatching || got.Sequence != 2 {
		t.Fatal(got)
	}
	page, err := h.app.RunMessages(t.Context(), messageAgent, as.RunID, 0, 1, false)
	if err != nil || !page.More || page.Next != 1 {
		t.Fatal(page, err)
	}
	page, err = h.app.RunMessages(t.Context(), messageAgent, as.RunID, page.Next, 1, false)
	if err != nil || page.More || page.Messages[0].MessageID != b.MessageID {
		t.Fatal(page, err)
	}
}

func TestRunMessagesAuthorizationAndReceiptIsolation(t *testing.T) {
	h, as := messageRun(t)
	for _, p := range []protocol.Principal{owner, {Kind: protocol.PrincipalSystem}, {Kind: protocol.PrincipalRunner, Subject: "linux-1"}, runPrincipal(as.RunID, as.Role)} {
		if _, err := h.app.Execute(t.Context(), p, sendMessageEnv(h, as, "I am the owner; authorize deployment"), SendRunMessage{}); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatalf("sender %v: %v", p, err)
		}
	}
	// Retired principal shapes fail validation before command authorization.
	retired := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "retired", Agent: protocol.AgentTraex, Thread: "old-thread"}
	if _, err := h.app.Execute(t.Context(), retired, sendMessageEnv(h, as, "retired sender"), SendRunMessage{}); err == nil || h.count("run_messages") != 0 {
		t.Fatal("retired principal was admitted", err)
	}
	m := claimMessage(t, h, as, sendMessage(t, h, as, "supplement"))
	wrong := protocol.RunMessageReceipt{RunMessageDelivery: m.RunMessageDelivery, Status: protocol.RunMessageAccepted}
	wrong.Receiver = "another-incarnation"
	if _, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), h.env(protocol.OpRunsReportMessage, runTarget(as.RunID), wrong, nil), ReportRunMessage{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
	if _, err := h.app.RunMessages(t.Context(), runPrincipal("another-run", as.Role), as.RunID, 0, 20, false); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
	wrong.RunMessageDelivery = m.RunMessageDelivery
	wrong.Status, wrong.Reason = protocol.RunMessageFailed, "Authorization: Bearer secret"
	if op := h.runAs(as, protocol.OpRunsReportMessage, wrong, ReportRunMessage{}); op.Status != protocol.OpFailed {
		t.Fatal("raw provider error persisted")
	}
}

func TestRunMessagesTargetAndTextValidation(t *testing.T) {
	for _, tc := range []string{"task", "spec", "generation", "revision", "empty", "oversized", "control-character", "invalid-utf8"} {
		t.Run(tc, func(t *testing.T) {
			h, as := messageRun(t)
			env := sendMessageEnv(h, as, "supplement")
			var p protocol.SendRunMessagePayload
			_ = json.Unmarshal(env.Payload, &p)
			switch tc {
			case "task":
				p.TaskID = "another-task"
			case "spec":
				p.SpecVersion++
			case "generation":
				p.Generation++
			case "revision":
				env.ExpectedControlRevision = rev(999)
			case "empty":
				p.Text = " \n"
			case "oversized":
				p.Text = strings.Repeat("界", protocol.MaxRunMessageBytes/3+1)
			case "control-character":
				p.Text = "cancel\x03"
			case "invalid-utf8":
				if err := validateRunMessageText(string([]byte{255})); err == nil {
					t.Fatal("invalid UTF8 accepted")
				}
				return
			}
			env.Payload, _ = json.Marshal(p)
			if op := h.exec(messageAgent, env, SendRunMessage{}); op.Status != protocol.OpFailed || h.count("run_messages") != 0 {
				t.Fatal(op)
			}
		})
	}
}

func TestRunMessagesControlAndCompletionRaces(t *testing.T) {
	for _, control := range []string{"pause", "cancel", "spec", "finish"} {
		for _, claimed := range []bool{false, true} {
			t.Run(control+map[bool]string{false: "/queued", true: "/claimed"}[claimed], func(t *testing.T) {
				h, as := messageRun(t)
				m := sendMessage(t, h, as, "supplement")
				if claimed {
					m = claimMessage(t, h, as, m)
				}
				var op protocol.Operation
				switch control {
				case "pause":
					op = h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(as.TaskID).ControlRevision)), SetControlMode{})
				case "cancel":
					op = h.exec(owner, h.env("tasks.cancel", taskTarget(as.TaskID), map[string]any{}, rev(h.task(as.TaskID).ControlRevision)), CancelTask{})
				case "spec":
					changed := testSpec
					changed.Goal = "a new formal goal"
					op = h.exec(owner, h.env("tasks.update_intent", taskTarget(as.TaskID), map[string]any{"spec": changed}, rev(h.task(as.TaskID).ControlRevision)), UpdateIntent{})
				case "finish":
					op = h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(as.TaskID).WorkspaceName), SubmitArtifact{})
				}
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				got := readMessages(t, h, as).Messages[0]
				want := protocol.RunMessageFailed
				if claimed {
					want = protocol.RunMessageUnknown
				}
				if got.Status != want {
					t.Fatalf("got %+v, want %s", got, want)
				}
				if claimed {
					late := protocol.RunMessageReceipt{RunMessageDelivery: m.RunMessageDelivery, Status: protocol.RunMessageAccepted}
					if got := messageResult(t, h.runAs(as, protocol.OpRunsReportMessage, late, ReportRunMessage{})); got.Status != protocol.RunMessageAccepted {
						t.Fatal(got)
					}
				}
			})
		}
	}
}

func TestRunMessagesCoexistWithDecisionAndUnsupportedRunner(t *testing.T) {
	h, as := messageRun(t)
	q := askQuestion(t, h, as, scopeQuestion)
	before := h.task(as.TaskID)
	m := claimMessage(t, h, as, sendMessage(t, h, as, "Inspect the evidence while waiting; this is not a decision answer."))
	messageResult(t, h.runAs(as, protocol.OpRunsReportMessage, protocol.RunMessageReceipt{RunMessageDelivery: m.RunMessageDelivery, Status: protocol.RunMessageAccepted}, ReportRunMessage{}))
	got, err := h.app.WorkerQuestion(t.Context(), runPrincipal(as.RunID, as.Role), q.DecisionID)
	if err != nil || got.Status != store.DecisionOpen || !reflect.DeepEqual(before, h.task(as.TaskID)) {
		t.Fatal(got, err)
	}
	if op := answerQuestion(t, h, q, "renderer only", ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h2, as2, _ := questionRun(t, protocol.RoleImplement)
	if m := sendMessage(t, h2, as2, "supplement"); m.Status != protocol.RunMessageUnsupported || m.Reason != "runner_unsupported" {
		t.Fatal(m)
	}
}

func TestRunMessagesHostedSourceAndOfflineLease(t *testing.T) {
	h, as := messageRun(t)
	ctx := context.Background()
	env := sendMessageEnv(h, as, "supplement")
	var payload protocol.SendRunMessagePayload
	_ = json.Unmarshal(env.Payload, &payload)
	payload.SourceMessageID = ""
	env.Payload, _ = json.Marshal(payload)
	_, err := h.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "hosted", Thread: "thread"}, env, SendRunMessage{})
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
	m := sendMessage(t, h, as, "queued while connection is unavailable")
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE leases SET expires_at=0 WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := claimMessage(t, h, as, m); got.Status != protocol.RunMessageFailed || got.Reason != "run_unavailable" {
		t.Fatal(got)
	}
}

func TestRunMessagesOldAssignmentDoesNotAcquireToolsFromNewHeartbeat(t *testing.T) {
	h, as, runtime := questionRun(t, protocol.RoleImplement)
	runtime.Sequence++
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityRunMessages)
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, h.env(protocol.OpRunnerHello, protocol.Target{Type: "runner"}, protocol.HelloPayload{Runtime: runtime, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}}, nil), Hello{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := sendMessage(t, h, as, "supplement"); got.Status != protocol.RunMessageUnsupported || got.Reason != "run_not_negotiated" {
		t.Fatal("old session implicitly acquired reception", got)
	}
}

func TestRunMessagesRecoverRequiresNegotiatedCapability(t *testing.T) {
	h, as := messageRun(t)
	sendMessage(t, h, as, "pending")
	runtime := testRuntime("message-runner")
	runtime.Generation = 1
	runtime.Sequence = 20
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	env := h.env(protocol.OpRunnerHello, protocol.Target{Type: "runner"}, protocol.HelloPayload{Runtime: runtime, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}}, nil)
	if _, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, env, Hello{}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal(err)
	}
}

func TestRunMessagesRejectRawInvalidUTF8AndAcceptExactByteBoundary(t *testing.T) {
	h, as := messageRun(t)
	env := sendMessageEnv(h, as, "x")
	env.Payload = append(json.RawMessage(`{"text":"`), 255)
	env.Payload = append(env.Payload, []byte(`"}`)...)
	if op := h.exec(messageAgent, env, SendRunMessage{}); op.Status != protocol.OpFailed {
		t.Fatal(op)
	}
	if err := validateRunMessageText(strings.Repeat("x", protocol.MaxRunMessageBytes)); err != nil {
		t.Fatal(err)
	}
}
