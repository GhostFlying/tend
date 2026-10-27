package runnerapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

var receiptAgent = protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "control", Thread: "original-control-session"}

func httpMessageRun(t *testing.T) (*env, protocol.Assignment, protocol.RunMessage, protocol.RunMessage) {
	t.Helper()
	e := newEnv(t, 10*time.Millisecond)
	e.createTask()
	runtime := &protocol.RunnerRuntime{InstanceID: "message-runner", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRunMessages}}
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "", protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: runtime}))
	runtime.Sequence++
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "", protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}))
	if as == nil || !as.RunMessages {
		t.Fatal("message support not negotiated", as)
	}
	e.mustOK(asRun(*as), e.envelope(protocol.OpRunsReportWorkspace, as.RunID, protocol.WorkspaceReport{Mounts: []protocol.ResolvedMount{{Path: "tend", ResolvedBaseSHA: baseSHA, ResolvedFrom: "candidate:main", Branch: as.Workspace.Branch}}}))
	e.mustOK(asRun(*as), e.envelope(protocol.OpRunsReportStarted, as.RunID, nil))
	send := func(text string) protocol.RunMessage {
		revision := int64(1)
		input, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "owner-client"}, e.envelope("conversation.submit_message", "", protocol.SubmitMessagePayload{Body: text}), app.SubmitMessage{})
		if err != nil || input.Status != protocol.OpAccepted {
			t.Fatal(input, err)
		}
		var source struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(input.Result, &source); err != nil {
			t.Fatal(err)
		}
		env := e.envelope(protocol.OpRunsSendMessage, as.RunID, protocol.SendRunMessagePayload{TaskID: as.TaskID, SpecVersion: as.SpecVersion, Generation: as.Generation, Text: text, SourceMessageID: source.MessageID, CommitSeq: 1})
		env.ExpectedControlRevision = &revision
		op, err := e.app.Execute(t.Context(), receiptAgent, env, app.SendRunMessage{})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		var m protocol.RunMessage
		if err := json.Unmarshal(op.Result, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m, queued := send("supplement within original scope"), send("later supplement")
	binding := protocol.RunMessageDelivery{MessageID: m.MessageID, Receiver: "original-receiver", ThreadID: "original-thread", TurnID: "original-turn"}
	if err := json.Unmarshal(e.mustOK(asRun(*as), e.envelope(protocol.OpRunsClaimMessage, as.RunID, binding)), &m); err != nil {
		t.Fatal(err)
	}
	return e, *as, m, queued
}

func receiptExecutionSnapshot(t *testing.T, e *env, as protocol.Assignment) any {
	t.Helper()
	var snapshot struct {
		Task  store.TaskRow
		Run   store.RunRow
		Lease store.LeaseRow
	}
	err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		if snapshot.Task, err = store.GetTask(t.Context(), tx.Q(), as.TaskID); err != nil {
			return err
		}
		if snapshot.Run, err = store.GetRun(t.Context(), tx.Q(), as.RunID); err != nil {
			return err
		}
		snapshot.Lease, err = store.GetLeaseByRun(t.Context(), tx.Q(), as.RunID)
		return err
	})
	if err != nil || snapshot.Lease.Status != store.LeaseReleased {
		t.Fatal("run lease was not released", snapshot, err)
	}
	return snapshot
}

func TestRunMessageLateReceiptOverHTTPAfterCompletionAndCancellation(t *testing.T) {
	for _, ending := range []string{"completed", "cancelled"} {
		t.Run(ending, func(t *testing.T) {
			e, as, m, queued := httpMessageRun(t)
			if ending == "completed" {
				e.mustOK(asRun(as), e.envelope(protocol.OpRunsSubmitArtifact, as.RunID, protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{{Repo: "tend", Path: "tend", BaseSHA: baseSHA, Branch: as.Workspace.Branch, CommitSHA: commitSHA, Changed: true}}}))
			} else {
				revision := int64(1)
				env := e.envelope("tasks.cancel", as.TaskID, map[string]any{})
				env.Target.Type, env.ExpectedControlRevision = "task", &revision
				if op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}, env, app.CancelTask{}); err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal(op, err)
				}
				e.mustOK(asRun(as), e.envelope(protocol.OpRunsRelease, as.RunID, protocol.ReleasePayload{Reason: "cancelled"}))
			}
			before := receiptExecutionSnapshot(t, e, as)
			page, err := e.app.RunMessages(t.Context(), receiptAgent, as.RunID, 0, 100, false)
			if err != nil || page.Messages[0].Status != protocol.RunMessageUnknown || page.Messages[1].Status != protocol.RunMessageFailed {
				t.Fatal(page, err)
			}
			// The narrow path cannot recreate claims or task/control authority,
			// even if the caller includes a genuine original delivery in the body.
			receipt := protocol.RunMessageReceipt{RunMessageDelivery: m.RunMessageDelivery, Status: protocol.RunMessageAccepted}
			for _, op := range []string{protocol.OpRunsClaimMessage, protocol.OpRunsReportStarted, protocol.OpRunsSubmitArtifact, protocol.OpRunsRelease, protocol.OpRunsReportTakeover, protocol.OpRunsAskOwner, protocol.OpDeliveriesPoll, protocol.OpRunnerEvents} {
				if status, _ := e.call(asRun(as), e.envelope(op, as.RunID, receipt)); status != http.StatusForbidden {
					t.Fatalf("historical token restored %s: %d", op, status)
				}
			}
			for _, path := range []string{"messages?wait=0", "questions", "files/file_missing", "observation"} {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.server.URL+Prefix+path, nil)
				req.Header.Set(HeaderRun, as.RunID)
				req.Header.Set("Authorization", "Bearer "+as.RunToken)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("historical token restored GET %s: %d", path, resp.StatusCode)
				}
			}
			for _, field := range []string{"message", "receiver", "thread", "turn", "unclaimed", "target"} {
				wrong := receipt
				target := as.RunID
				switch field {
				case "message":
					wrong.MessageID = "msg_missing"
				case "receiver":
					wrong.Receiver = "replacement"
				case "thread":
					wrong.ThreadID = "replacement"
				case "turn":
					wrong.TurnID = "replacement"
				case "unclaimed":
					wrong.RunMessageDelivery = queued.RunMessageDelivery
				case "target":
					target = "run_other"
				}
				if status, _ := e.call(asRun(as), e.envelope(protocol.OpRunsReportMessage, target, wrong)); status != http.StatusForbidden {
					t.Fatalf("historical receipt accepted incorrect %s: %d", field, status)
				}
			}
			env := e.envelope(protocol.OpRunsReportMessage, as.RunID, receipt)
			if status, _ := e.call(credential{HeaderRun, as.RunID, "wrong-token"}, env); status != http.StatusForbidden {
				t.Fatal("forged historical token accepted", status)
			}
			if status, _ := e.call(asHost(), env); status != http.StatusForbidden {
				t.Fatal("host token used as a historical run", status)
			}
			e.peers.set(&netx.Identity{Node: "other-node"})
			if status, _ := e.call(asRun(as), env); status != http.StatusForbidden {
				t.Fatal("historical token bypassed node checks", status)
			}
			e.peers.set(runnerNode)
			first := e.mustOK(asRun(as), env)
			if again := e.mustOK(asRun(as), env); string(first) != string(again) {
				t.Fatal("historical receipt replay changed result")
			}
			page, err = e.app.RunMessages(t.Context(), receiptAgent, as.RunID, 0, 100, false)
			if err != nil || page.Messages[0].Status != protocol.RunMessageAccepted || len(page.Messages[0].History) != 4 || page.Messages[1].Status != protocol.RunMessageFailed {
				t.Fatal("receipt missing from ledger or recreated later delivery", page, err)
			}
			if after := receiptExecutionSnapshot(t, e, as); !reflect.DeepEqual(before, after) {
				t.Fatal("historical receipt changed task, run or lease", before, after)
			}
			if strings.Contains(e.logs.String(), "supplement within original scope") || strings.Contains(e.logs.String(), as.RunToken) {
				t.Fatal("receipt logging leaked text or token")
			}
			// Revocation still prevents retrying even a previously successful ID.
			e.system("config.sync", app.ConfigPayload{}, app.SyncConfig{})
			if status, _ := e.call(asRun(as), env); status != http.StatusForbidden {
				t.Fatal("revoked host retained receipt authentication", status)
			}
		})
	}
}
