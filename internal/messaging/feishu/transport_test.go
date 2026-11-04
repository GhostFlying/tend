package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestTransportSignedCallbackDurableMessageOutboxAndRevokedOwner(t *testing.T) {
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 81)
	database, err := feishuFixture.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	a := app.New(database, ids, clock.Now)
	var sends atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"fixture","expire":7200}`)
			return
		}
		sends.Add(1)
		_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"provider-output"}}`)
	}))
	defer provider.Close()
	transport, err := NewTransport(a, APIConfig{AppID: "app-fixture", AppSecret: "secret", BaseURL: provider.URL, Now: clock.Now}, CallbackConfig{AppID: "app-fixture", TenantKey: "tenant-fixture", VerificationToken: "verify-fixture", EncryptKey: "encryption-fixture", Now: clock.Now}, []string{"owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.ConfigureFeishuRuntime(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "fixture"}
	execute := func(operation string, target protocol.Target, payload any, revision *int64, command app.Command) protocol.Operation {
		t.Helper()
		raw, _ := json.Marshal(payload)
		op, err := a.Execute(t.Context(), owner, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw, ExpectedControlRevision: revision}, command)
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
		return op
	}
	revision := int64(1)
	execute("messaging.feishu_bind", protocol.Target{Type: "channel", ID: "feishu"}, protocol.BindFeishu{AppID: "app-fixture", TenantKey: "tenant-fixture", SenderID: "owner-fixture", ChatID: "oc-1"}, &revision, app.FeishuCommand{})
	body := []byte(strings.ReplaceAll(string(messageFixture()), `,"parent_id":"om-parent"`, ""))
	for range 2 {
		w := httptest.NewRecorder()
		transport.ServeHTTP(w, signedRequest(transport.callback, body, clock.Now().Unix()))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String(), transport.Receive(t.Context(), Incoming{EventID: "ev-1", MessageID: "om-1", AppID: "app-fixture", TenantKey: "tenant-fixture", SenderID: "owner-fixture", ChatID: "oc-1", Text: "hello"}))
		}
	}
	page, err := a.MessagingMessages(t.Context(), 0, "", "", "feishu")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	inputs, err := a.FeishuInputs(t.Context(), 0, 1, "accepted")
	if err != nil || len(inputs.Items) != 1 || inputs.Items[0].MessageID != page.Items[0].MessageID {
		t.Fatal(inputs, err)
	}
	execute("messaging.notify", protocol.Target{Type: "message"}, protocol.MessagingPublication{Body: "Resolved", Purpose: "final", Reason: "requested_result"}, nil, app.MessagingCommand{Action: "notify"})
	delivery, _, ok, err := a.NextDelivery(t.Context(), app.FeishuDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	result := transport.Deliver(t.Context(), delivery)
	if result.Outcome != app.Delivered || result.ExternalMessageReference != "provider-output" {
		t.Fatal(result)
	}
	if err = a.FinishDelivery(t.Context(), delivery, result); err != nil {
		t.Fatal(err)
	}
	receipts, err := a.MessagingReceipts(t.Context(), deliveryMessageID(delivery.Payload))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, receipt := range receipts.Deliveries {
		if receipt.ChannelID == "feishu" {
			found = true
			if receipt.ExternalMessageReference != "provider-output" {
				t.Fatal("opaque receipt missing", receipt)
			}
		}
	}
	if !found {
		t.Fatal("Feishu delivery not visible")
	}
	// Receipt reconciliation has no UUID lookup and must not re-send a message.
	if got := transport.Reconcile(t.Context(), delivery); got.Outcome != app.Unknown || sends.Load() != 1 {
		t.Fatal(got, sends.Load())
	}
	transport.owners = nil
	if err = transport.Receive(t.Context(), Incoming{EventID: "new", MessageID: "new", AppID: "app-fixture", TenantKey: "tenant-fixture", SenderID: "owner-fixture", ChatID: "oc-1", Text: "forbidden"}); err == nil {
		t.Fatal("removed owner accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan struct{})
	go func() { transport.Run(ctx); close(done) }()
	<-done
}

func deliveryMessageID(raw []byte) string {
	var p app.FeishuPart
	_ = json.Unmarshal(raw, &p)
	return p.MessageID
}
