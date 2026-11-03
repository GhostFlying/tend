package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func websocketApp(t *testing.T) (*app.App, *store.Store, *testkit.Clock) {
	t.Helper()
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 81)
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := app.New(db, ids, clock.Now)
	if err = a.ConfigureFeishuRuntime(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	rev := int64(1)
	raw, _ := json.Marshal(protocol.BindFeishu{AppID: "app-fixture", TenantKey: "tenant-fixture", SenderID: "owner-fixture", ChatID: "oc-1"})
	cmd := app.FeishuCommand{}
	op, err := a.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "fixture"}, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: protocol.Target{Type: "channel", ID: "feishu"}, ExpectedControlRevision: &rev, Payload: raw}, cmd)
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	return a, db, clock
}

func TestWebSocketSDKReconnectDeduplicatesAndJoins(t *testing.T) {
	a, db, clock := websocketApp(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	acknowledgements := make(chan int, 4)
	errorsSeen := make(chan error, 4)
	closed := make(chan struct{}, 4)
	var connections atomic.Int32
	var origin string
	upgrader := websocket.Upgrader{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == larkws.GenEndpointUri {
			var request larkws.BootstrapRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				errorsSeen <- err
				w.WriteHeader(400)
				return
			}
			if request.AppID != "app-fixture" || request.AppSecret != "isolated-secret" {
				w.WriteHeader(403)
				return
			}
			_ = json.NewEncoder(w).Encode(larkws.EndpointResp{Code: 0, Data: &larkws.Endpoint{Url: "ws" + strings.TrimPrefix(origin, "http") + "/events?device_id=fixture&service_id=1", ClientConfig: &larkws.ClientConfig{ReconnectCount: 3, PingInterval: 600}}})
			return
		}
		if r.URL.Path != "/events" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errorsSeen <- err
			return
		}
		defer conn.Close()
		defer func() { closed <- struct{}{} }()
		n := connections.Add(1)
		payload := []byte(strings.ReplaceAll(string(messageFixture()), `,"parent_id":"om-parent"`, ""))
		frame := larkws.Frame{Method: int32(larkws.FrameTypeData), Service: 1, Headers: []larkws.Header{{Key: larkws.HeaderType, Value: string(larkws.MessageTypeEvent)}, {Key: larkws.HeaderMessageID, Value: "transport-message"}, {Key: larkws.HeaderSum, Value: "1"}, {Key: larkws.HeaderSeq, Value: "0"}}, Payload: payload}
		raw, err := frame.Marshal()
		if err != nil {
			errorsSeen <- err
			return
		}
		if err = conn.WriteMessage(websocket.BinaryMessage, raw); err != nil {
			errorsSeen <- err
			return
		}
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var response larkws.Frame
			if err = response.Unmarshal(raw); err != nil {
				errorsSeen <- err
				return
			}
			if response.Method != int32(larkws.FrameTypeData) {
				continue
			}
			var receipt larkws.Response
			if err = json.Unmarshal(response.Payload, &receipt); err != nil {
				errorsSeen <- err
				return
			}
			acknowledgements <- receipt.StatusCode
			if n == 1 {
				return
			} // The second connection redelivers the identical provider event.
		}
	}))
	defer gateway.Close()
	origin = gateway.URL
	transport, err := newWebSocketTransport(a, APIConfig{AppID: "app-fixture", AppSecret: "isolated-secret", Now: clock.Now}, "tenant-fixture", []string{"owner"}, larkws.WithDomain(gateway.URL), larkws.WithHttpClient(gateway.Client()))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	transport.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hooks/feishu", nil))
	if recorder.Code != 404 {
		t.Fatal("websocket mode exposed HTTP callback")
	}
	done := make(chan struct{})
	go func() { defer close(done); transport.Run(ctx) }()
	for range 2 {
		select {
		case code := <-acknowledgements:
			if code != 200 {
				t.Fatalf("SDK ack=%d", code)
			}
		case err := <-errorsSeen:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	binding, err := a.FeishuBinding(ctx)
	if err != nil || binding.RuntimeHealth != "connected" {
		t.Fatal(binding, err)
	}
	if err = db.Read(ctx, func(tx *store.ReadTx) error {
		var inputs, messages int
		if err := tx.Q().QueryRowContext(ctx, `SELECT count(*) FROM feishu_inputs`).Scan(&inputs); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages`).Scan(&messages); err != nil {
			return err
		}
		if inputs != 1 || messages != 1 {
			t.Fatalf("redelivery duplicated input=%d messages=%d", inputs, messages)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("transport did not join SDK and recovery workers")
	}
	for range 2 {
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("SDK socket still open")
		}
	}
}

func TestWebSocketRejectsTenantAndOwnerAndPreservesUnknown(t *testing.T) {
	a, db, clock := websocketApp(t)
	transport, err := newWebSocketTransport(a, APIConfig{AppID: "app-fixture", AppSecret: "isolated-secret", Now: clock.Now}, "tenant-fixture", []string{"owner"})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(strings.ReplaceAll(string(messageFixture()), `,"parent_id":"om-parent"`, ""))
	handler := webSocketEventHandler(transport)
	for _, bad := range []string{strings.ReplaceAll(string(body), "tenant-fixture", "different-tenant"), strings.ReplaceAll(string(body), "owner-fixture", "unbound-person"), strings.ReplaceAll(string(body), `"chat_type":"p2p"`, `"chat_type":"group"`)} {
		if _, err = handler.Do(t.Context(), []byte(bad)); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	if _, err = handler.Do(t.Context(), body); err != nil {
		t.Fatal(err)
	}
	if err = db.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE request_records SET status='unknown' WHERE request_id=(SELECT request_id FROM feishu_inputs LIMIT 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = handler.Do(t.Context(), body); err == nil {
		t.Fatal("unknown business receipt acknowledged")
	}
	revoked := *transport
	revoked.owners = []string{"another-owner"}
	if _, err = webSocketEventHandler(&revoked).Do(t.Context(), body); err == nil {
		t.Fatal("revoked owner accepted")
	}
}

func TestWebSocketCredentialFailureDoesNotStopRecovery(t *testing.T) {
	a, _, clock := websocketApp(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":514,"msg":"invalid credentials"}`)
	}))
	defer provider.Close()
	transport, err := newWebSocketTransport(a, APIConfig{AppID: "app-fixture", AppSecret: "wrong-secret", Now: clock.Now}, "tenant-fixture", []string{"owner"}, larkws.WithDomain(provider.URL), larkws.WithHttpClient(provider.Client()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); transport.Run(ctx) }()
	for {
		binding, err := a.FeishuBinding(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if binding.RuntimeHealth == "connection_failed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case <-done:
		t.Fatal("channel error stopped its durable recovery lifecycle")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("credential failure prevented cancellation")
	}
}

func TestWebSocketSDKLogsDoNotExposeSecrets(t *testing.T) {
	const sentinel = "TEND_PRIVATE_FEISHU_TICKET_SENTINEL"
	if os.Getenv("TEND_TEST_FEISHU_LOG_CHILD") == "1" {
		a, _, clock := websocketApp(t)
		var origin string
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == larkws.GenEndpointUri {
				_ = json.NewEncoder(w).Encode(larkws.EndpointResp{Code: 0, Data: &larkws.Endpoint{Url: "ws" + strings.TrimPrefix(origin, "http") + "/reject?device_id=fixture&service_id=1&ticket=" + sentinel}})
				return
			}
			w.Header().Set(larkws.HeaderHandshakeStatus, "514")
			w.Header().Set(larkws.HeaderHandshakeMsg, sentinel)
			http.Error(w, sentinel, http.StatusUnauthorized)
		}))
		defer provider.Close()
		origin = provider.URL
		transport, err := newWebSocketTransport(a, APIConfig{AppID: "app-fixture", AppSecret: sentinel, Now: clock.Now}, "tenant-fixture", []string{"owner"}, larkws.WithDomain(provider.URL), larkws.WithHttpClient(provider.Client()), larkws.WithAutoReconnect(false))
		if err != nil {
			t.Fatal("cannot construct fixture transport")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); transport.Run(ctx) }()
		for {
			b, err := a.FeishuBinding(ctx)
			if err != nil {
				t.Fatal("cannot read fixture health")
			}
			if b.RuntimeHealth == "connection_failed" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("fixture connection did not settle")
			case <-time.After(5 * time.Millisecond):
			}
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("fixture connection did not join")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestWebSocketSDKLogsDoNotExposeSecrets$", "-test.count=1")
	command.Env = append(os.Environ(), "TEND_TEST_FEISHU_LOG_CHILD=1")
	output, err := command.CombinedOutput()
	if strings.Contains(string(output), sentinel) {
		t.Fatal("SDK exposed private credential or ticket in process output")
	}
	if err != nil {
		t.Fatalf("SDK logging fixture failed: %v", err)
	}
}
