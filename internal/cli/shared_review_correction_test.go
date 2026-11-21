package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueReviewCorrectionRetainsActualReportAndOriginalRequest(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	input := `{"expected_assignment_epoch":3,"expected_review_revision":11,"delivery_id":"pr","action":"correct","reviewer_task_id":"original-reviewer-report","reason":"Relay the actual immutable correction; retain the formal round and old report"}`
	if code := e.main("issue", "review-delivery", "issue", "--revision", "7", "--epoch", "3", "--data", input, "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var original protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	var payload protocol.UpdateSharedDeliveryReviewPayload
	if err := json.Unmarshal(original.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Action != "correct" || payload.ReviewerTaskID != "original-reviewer-report" || payload.ExpectedReviewRevision == nil || *payload.ExpectedReviewRevision != 11 || original.ExpectedControlRevision == nil || *original.ExpectedControlRevision != 7 || len(f.requests) != 0 {
		t.Fatal(original, payload, f.requests)
	}
	file := filepath.Join(t.TempDir(), "original-correction.json")
	if err := os.WriteFile(file, e.out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var got protocol.Envelope
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		wantHash, wantErr := protocol.PayloadHash(&original)
		gotHash, gotErr := protocol.PayloadHash(&got)
		if wantErr != nil || gotErr != nil || wantHash != gotHash || got.RequestID != original.RequestID || got.ExpectedControlRevision == nil || *got.ExpectedControlRevision != 7 {
			t.Error("recovery changed the report reference or CAS", got)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: original.RequestID, Status: protocol.OpSucceeded}})
	})
	e.out.Reset()
	if code := e.main("operation", "submit", "--file", file, "--json"); code != exitOK || len(f.requests) != 1 {
		t.Fatal(code, e.errOut.String(), f.requests)
	}
}

func TestIssueDeliveryReviewReceiptReadsOriginalRequest(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	const request = "req_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	f.on("GET /api/v1/issues/issue/delivery-reviews", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("request_id") != request || r.URL.Query().Get("delivery_id") != "pr" {
			t.Error("receipt lookup changed the original selector", r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"receipt": protocol.SharedReviewRequestReceipt{RequestID: request, Status: "unknown"}, "items": []any{}, "more": false})
	})
	if code := e.main("issue", "delivery-reviews", "issue", "--delivery", "pr", "--review-request-id", request); code != exitOK || len(f.requests) != 1 {
		t.Fatal(code, e.errOut.String(), f.requests)
	}
	var page protocol.SharedDeliveryReviewPage
	if err := json.Unmarshal(e.out.Bytes(), &page); err != nil || page.Receipt == nil || page.Receipt.RequestID != request || page.Receipt.Status != "unknown" {
		t.Fatal(page, err)
	}
}
