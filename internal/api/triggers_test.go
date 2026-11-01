package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/webhookauth"
)

func TestWebhookIntakeAuthenticationDedupAndReadonlyDiagnosis(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	keys := &webhookauth.Files{Dir: filepath.Join(t.TempDir(), "keys")}
	e.server.cfg.WebhookKeys = keys
	app.WithWebhookKeys(keys)(e.app)
	key := bytes.Repeat([]byte("k"), 32)
	ref := e.ids.New(protocol.PrefixRequest)
	if err := keys.Put(t.Context(), ref, key); err != nil {
		t.Fatal(err)
	}
	payload := protocol.WebhookSubscriptionPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Observe a result", Instruction: "Read the current Issue before action", KeyRef: ref}
	code, response := e.op(token, e.envelope("webhooks.create", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, payload))
	if code != 200 || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatal(code, response)
	}
	var sub protocol.WebhookSubscription
	if err := json.Unmarshal(response.Operation.Result, &sub); err != nil {
		t.Fatal(err)
	}
	intake := httptest.NewServer(e.server.WebhookHandler())
	defer intake.Close()
	send := func(delivery, body string, signed bool) (int, protocol.WebhookReceipt) {
		t.Helper()
		r, _ := http.NewRequest("POST", intake.URL+"/hooks/v1/"+sub.ID, bytes.NewBufferString(body))
		stamp := fmt.Sprint(e.clock.Now().Unix())
		r.Header.Set("X-Tend-Timestamp", stamp)
		r.Header.Set("X-Tend-Delivery", delivery)
		if signed {
			m := hmac.New(sha256.New, key)
			fmt.Fprintf(m, "%s.%s.%d.%s.%s", sub.ID, stamp, len(delivery), delivery, body)
			r.Header.Set("X-Tend-Signature", "sha256="+hex.EncodeToString(m.Sum(nil)))
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var receipt protocol.WebhookReceipt
		_ = json.NewDecoder(res.Body).Decode(&receipt)
		return res.StatusCode, receipt
	}
	if status, _ := send("delivery-1", `{"state":"done"}`, false); status != 401 {
		t.Fatal(status)
	}
	status, first := send("delivery-1", `{"state":"done"}`, true)
	if status != 202 || first.EventID == "" {
		t.Fatal(status, first)
	}
	status, replay := send("delivery-1", `{"state":"done"}`, true)
	if status != 202 || replay.EventID != first.EventID || e.count(`SELECT COUNT(*) FROM webhook_receipts`) != 1 {
		t.Fatal(status, replay)
	}
	if status, _ := send("delivery-1", `{"state":"changed"}`, true); status != 409 {
		t.Fatal("identity conflict", status)
	}
	if status := e.do("GET", "/api/v1/diagnostics/events", "", nil, nil); status != 401 {
		t.Fatal("diagnostics leaked", status)
	}
	var events protocol.TriggerEventPage
	if status := e.do("GET", "/api/v1/diagnostics/events?issue_id="+issue.IssueID, token, nil, &events); status != 200 || len(events.Items) != 1 {
		t.Fatal(status, events)
	}
	var detail protocol.TriggerEvent
	if status := e.do("GET", "/api/v1/diagnostics/events/"+first.EventID, token, nil, &detail); status != 200 || len(detail.Steps) == 0 {
		t.Fatal(status, detail)
	}
	var schedules protocol.TriggerSchedulePage
	if status := e.do("GET", "/api/v1/diagnostics/schedules", token, nil, &schedules); status != 200 || len(schedules.Items) != 1 || schedules.Items[0].Source != "webhook" {
		t.Fatal(status, schedules)
	}
	r, _ := http.NewRequest("GET", intake.URL+"/api/v1/issues", nil)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("external listener exposed owner API", res.StatusCode)
	}
}

func TestWebhookSignatureBindsDeliveryAndBodyBoundary(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	keys := &webhookauth.Files{Dir: filepath.Join(t.TempDir(), "keys")}
	e.server.cfg.WebhookKeys = keys
	app.WithWebhookKeys(keys)(e.app)
	key := bytes.Repeat([]byte("k"), 32)
	ref := e.ids.New(protocol.PrefixRequest)
	if err := keys.Put(t.Context(), ref, key); err != nil {
		t.Fatal(err)
	}
	p := protocol.WebhookSubscriptionPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Observe", Instruction: "Read current facts", KeyRef: ref}
	code, res := e.op(token, e.envelope("webhooks.create", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, p))
	if code != 200 || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatal(code, res)
	}
	var sub protocol.WebhookSubscription
	if err := json.Unmarshal(res.Operation.Result, &sub); err != nil {
		t.Fatal(err)
	}
	intake := httptest.NewServer(e.server.WebhookHandler())
	defer intake.Close()
	stamp := fmt.Sprint(e.clock.Now().Unix())
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "%s.%s.%d.%s.%s", sub.ID, stamp, len("delivery-1"), "delivery-1", "1.2")
	signature := "sha256=" + hex.EncodeToString(m.Sum(nil))
	send := func(delivery, body string) int {
		r, _ := http.NewRequest("POST", intake.URL+"/hooks/v1/"+sub.ID, bytes.NewBufferString(body))
		r.Header.Set("X-Tend-Timestamp", stamp)
		r.Header.Set("X-Tend-Delivery", delivery)
		r.Header.Set("X-Tend-Signature", signature)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if code := send("delivery-1", "1.2"); code != 202 {
		t.Fatal(code)
	}
	if code := send("delivery-1.1", "2"); code != 401 {
		t.Fatalf("reused signature authenticated altered delivery and body: HTTP %d; receipts=%d", code, e.count("SELECT COUNT(*) FROM webhook_receipts"))
	}
}
