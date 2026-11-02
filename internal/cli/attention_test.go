package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAttentionResponsibilityCLIUsesIndependentRevisionAndReplaysExactRequest(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/attentions/attention-original", protocol.AttentionView{Attention: protocol.Attention{AttentionID: "attention-original", Revision: 9, Responsibility: &protocol.AttentionResponsibility{Revision: 3, AssignmentEpoch: 7}}})
	if code := te.main("attention", "resolve", "attention-original", "--data", `{"version":2,"option":"raise the budget"}`, "--reason", "Reviewed independent rejection", "--dry-run"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var original protocol.Envelope
	if err := json.Unmarshal(te.out.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	var payload protocol.HandleAttentionPayload
	if err := json.Unmarshal(original.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if *original.ExpectedControlRevision != 3 || payload.ExpectedAssignmentEpoch != 7 || payload.Version != 2 || payload.Reason == "" || original.Operation != "attentions.resolve" {
		t.Fatal(original, payload)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("dry run sent mutation")
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var received protocol.Envelope
		if err := json.Unmarshal(body, &received); err != nil {
			t.Error(err)
		}
		before, _ := protocol.PayloadHash(&original)
		after, _ := protocol.PayloadHash(&received)
		if before != after || received.RequestID != original.RequestID || !reflect.DeepEqual(received.ExpectedControlRevision, original.ExpectedControlRevision) {
			t.Error("replay rebuilt original control", received)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, RequestID: received.RequestID}})
	})
	body := te.out.String()
	te.out.Reset()
	if code := te.main("attention", "replay", "--data", body); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	if len(f.seen("GET /api/v1/attentions/attention-original")) != 1 || len(f.seen("POST /api/v1/ops")) != 1 {
		t.Fatal("replay refreshed current responsibility")
	}
}

func TestAttentionResponsibilityCLIRetryRequiresEpochAndFiltersBeforePaging(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	request := te.IDs.New(protocol.PrefixRequest)
	if code := te.main("attention", "escalate", "attention-original", "--request-id", request, "--revision", "3", "--reason", "Unsolved"); code != exitUsage {
		t.Fatal(code, te.errOut.String())
	}
	if len(f.requests) != 0 {
		t.Fatal("incomplete recovery sent request")
	}
	te.out.Reset()
	if code := te.main("attention", "escalate", "attention-original", "--request-id", request, "--revision", "3", "--epoch", "0", "--reason", "Top coordinator cannot resolve", "--dry-run"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	f.on("GET /api/v1/attentions", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		if q.Get("recipient") != "agent" || q.Get("responsible_issue_id") != "issue-parent" || q.Get("after") != "cursor-original" || q.Get("limit") != "2" {
			t.Error(q)
		}
		_ = json.NewEncoder(w).Encode(protocol.AttentionPage{})
	})
	if code := te.main("attention", "list", "--recipient", "agent", "--responsible-issue", "issue-parent", "--limit", "2", "--after", "cursor-original"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
}

func TestAttentionResponsibilityCLIHonorsPayloadEpochDuringRevisionLookup(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/attentions/attention-original", protocol.AttentionView{Attention: protocol.Attention{AttentionID: "attention-original", Revision: 5, Responsibility: &protocol.AttentionResponsibility{Revision: 3, AssignmentEpoch: 7}}})
	if code := te.main("attention", "escalate", "attention-original", "--data", `{"expected_assignment_epoch":6,"reason":"Original responsibility"}`, "--dry-run"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var env protocol.Envelope
	if err := json.Unmarshal(te.out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	var p protocol.HandleAttentionPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.ExpectedAssignmentEpoch != 6 {
		t.Fatal("implicit lookup replaced explicit epoch", p)
	}
}
