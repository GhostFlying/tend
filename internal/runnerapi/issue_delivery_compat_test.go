package runnerapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

// This test uses only the pre-existing delivery messages and commands. It is
// retains the opaque payload across both supported runner generations.
func TestIssueDeliveryObserverPreservesSupportedRunnerProtocols(t *testing.T) {
	for _, version := range []int64{protocol.PreviousRunnerProtocol, protocol.CurrentRunnerProtocol} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			e := newEnv(t, time.Millisecond)
			owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test-owner"}
			execute := func(command app.Command, target protocol.Target, payload any, revision *int64) protocol.Operation {
				t.Helper()
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				op, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: command.Operation(), Target: target, ExpectedControlRevision: revision, Payload: raw}, command)
				if err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal(op, err)
				}
				return op
			}
			op := execute(app.CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Track publication", Project: "default", Intent: "Retain provider facts", Deliveries: []protocol.IssueDelivery{{DeliveryID: "delivery", Provider: "github", Repository: "example/project", Number: 7}}}}, nil)
			var issue protocol.Issue
			if err := json.Unmarshal(op.Result, &issue); err != nil {
				t.Fatal(err)
			}
			runtime := &protocol.RunnerRuntime{InstanceID: "observer", Generation: 1, Sequence: 1, Protocol: version, ProtocolMin: 0, ProtocolMax: version, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityDeliveryGitHub}}
			e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "linux-1", protocol.HelloPayload{Runtime: runtime, Held: []protocol.HeldRun{}}))
			// This frozen shape deliberately does not acquire canonical owner fields.
			type oldAssignment struct {
				WorkItemID string                 `json:"work_item_id"`
				Delivery   protocol.IssueDelivery `json:"delivery"`
				Generation int64                  `json:"generation"`
				Nonce      string                 `json:"nonce"`
			}
			type oldPoll struct {
				Assignments []oldAssignment `json:"assignments"`
			}
			poll := func() oldAssignment {
				t.Helper()
				raw := e.mustOK(asHost(), e.envelope(protocol.OpDeliveriesPoll, "linux-1", protocol.DeliveryPollPayload{InstanceID: "observer"}))
				var result oldPoll
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&result); err != nil || len(result.Assignments) != 1 {
					t.Fatal(string(raw), err)
				}
				return result.Assignments[0]
			}
			first := poll()
			if first.WorkItemID != issue.IssueID {
				t.Fatal(first)
			}
			report := func(a oldAssignment, state string) bool {
				t.Helper()
				raw := e.mustOK(asHost(), e.envelope(protocol.OpDeliveriesReport, "linux-1", protocol.DeliveryReportPayload{InstanceID: "observer", WorkItemID: a.WorkItemID, DeliveryID: a.Delivery.DeliveryID, Generation: a.Generation, Nonce: a.Nonce, Snapshot: &protocol.DeliverySnapshot{State: state, HeadSHA: "head"}}))
				var result protocol.DeliveryReportResult
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				return result.Accepted
			}
			if !report(first, "open") {
				t.Fatal("original observer report rejected")
			}
			execute(app.RefreshDelivery{}, protocol.Target{Type: "work_item", ID: issue.IssueID}, protocol.RefreshDeliveryPayload{DeliveryID: "delivery"}, &issue.Revision)
			next := poll()
			if next.Generation <= first.Generation || next.Nonce == first.Nonce {
				t.Fatal(first, next)
			}
			if !report(next, "merged") || report(first, "closed") {
				t.Fatal("observer generation/nonce contract changed")
			}
		})
	}
}
