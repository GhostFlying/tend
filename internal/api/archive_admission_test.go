package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type archiveSlowBody struct {
	reader           *bytes.Reader
	entered, release chan struct{}
	once             sync.Once
	reads            atomic.Int64
}

func (b *archiveSlowBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.reader.Read(p)
}
func (b *archiveSlowBody) Close() error { return nil }

func TestArchivePrepareRejectsBodyBeforeReadButFinishesAdmittedUpload(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	id, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	newRequest := func(body io.ReadCloser) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/conversation/attachments?name=note.txt&media_type=text%2Fplain&request_id="+e.ids.New(protocol.PrefixRequest), body)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set(deploymentHeader, id)
		return r
	}
	body := &archiveSlowBody{reader: bytes.NewReader([]byte("admitted original")), entered: make(chan struct{}), release: make(chan struct{})}
	first := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { defer close(finished); e.server.Handler().ServeHTTP(first, newRequest(body)) }()
	<-body.entered
	if _, err := e.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: e.ids.New(protocol.PrefixRequest)}, true); err != nil {
		t.Fatal(err)
	}
	v, err := e.app.ArchiveOnlineStatus(t.Context())
	if err != nil || v.ActiveOwnerRequests != 1 {
		t.Fatal(v, err)
	}
	next := &archiveSlowBody{reader: bytes.NewReader([]byte("unadmitted")), entered: make(chan struct{}), release: make(chan struct{})}
	refused := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(refused, newRequest(next))
	if refused.Code != http.StatusServiceUnavailable || next.reads.Load() != 0 {
		t.Fatal("new body was read", refused.Code, next.reads.Load(), refused.Body.String())
	}
	close(body.release)
	<-finished
	if first.Code != http.StatusOK {
		t.Fatal(first.Code, first.Body.String())
	}
	v, err = e.app.ArchiveOnlineStatus(t.Context())
	if err != nil || v.ActiveOwnerRequests != 0 {
		t.Fatal(v, err)
	}
	if code := e.do("GET", "/api/v1/session", token, nil, nil); code != 200 {
		t.Fatal("read was blocked", code)
	}
	if code := e.do("POST", "/api/v1/session", "", protocol.SessionRequest{ClientID: clientID, Scope: protocol.ScopeOwner}, nil); code != 200 {
		t.Fatal("read access setup blocked", code)
	}
}

func TestArchiveRefusalDoesNotWaitForUnadmittedHTTPBody(t *testing.T) {
	for _, framing := range []string{"Content-Length: 8", "Transfer-Encoding: chunked"} {
		t.Run(framing, func(t *testing.T) {
			e := newEnv(t)
			token := e.login(protocol.ScopeOwner)
			deployment, err := e.app.DeploymentID(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: e.ids.New(protocol.PrefixRequest)}, true); err != nil {
				t.Fatal(err)
			}
			hs := httptest.NewServer(e.server.Handler())
			defer hs.Close()
			conn, err := net.DialTimeout("tcp", hs.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Send authenticated headers only. The unadmitted upload never sends
			// its body or EOF; a real HTTP server must not wait for either.
			if _, err := fmt.Fprintf(conn, "POST /api/v1/conversation/attachments?name=unadmitted.txt&media_type=text%%2Fplain&request_id=%s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n%s: %s\r\n%s\r\n\r\n", e.ids.New(protocol.PrefixRequest), hs.Listener.Addr(), token, deploymentHeader, deployment, framing); err != nil {
				t.Fatal(err)
			}
			if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Errorf("prepare refusal waited for unread body: %v", err)
			} else {
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusServiceUnavailable || !bytes.Contains(body, []byte(protocol.CodeInvalidTransition)) {
					t.Errorf("refusal = %d %q, %v", response.StatusCode, body, err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := hs.Config.Shutdown(ctx); err != nil {
				t.Errorf("unadmitted upload kept HTTP shutdown active: %v", err)
			}
			status, err := e.app.ArchiveOnlineStatus(t.Context())
			if err != nil || status.ActiveOwnerRequests != 0 {
				t.Fatal("refused body became an admitted request", status, err)
			}
		})
	}
}
