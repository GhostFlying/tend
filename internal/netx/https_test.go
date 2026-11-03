package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// MaxConnsPerHost=1 is an explicit condition, not a claim about a particular
// browser's pool size. Use real TLS and Server.Serve on both paths.
func TestHTTPSMultiplexesReadsBesideHeldEventStream(t *testing.T) {
	for _, multiplex := range []bool{false, true} {
		t.Run(map[bool]string{false: "original-http1", true: "multiplexed"}[multiplex], func(t *testing.T) {
			seed := httptest.NewTLSServer(http.NotFoundHandler())
			cert := seed.TLS.Certificates[0]
			leaf := seed.Certificate()
			seed.Close()
			roots := x509.NewCertPool()
			roots.AddCert(leaf)
			raw, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var certificateCalls, reads, connections atomic.Int32
			var streamPeer, readPeer atomic.Value
			streamStopped := make(chan struct{})
			getCertificate := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { certificateCalls.Add(1); return &cert, nil }
			var listener net.Listener
			if multiplex {
				listener = httpsListener(raw, getCertificate)
			} else {
				listener = tls.NewListener(raw, &tls.Config{GetCertificate: getCertificate})
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/events" {
					streamPeer.Store(r.RemoteAddr)
					defer close(streamStopped)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: start\ndata: {}\n\n")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				readPeer.Store(r.RemoteAddr)
				reads.Add(1)
				w.Header().Set("X-Observed-Authorization", r.Header.Get("Authorization"))
				_, _ = io.WriteString(w, `{"items":[]}`)
			}), ConnState: func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}}
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				_ = server.Close()
				select {
				case err := <-done:
					if !errors.Is(err, http.ErrServerClosed) {
						t.Errorf("Serve: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("server did not stop")
				}
			})
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true, MaxConnsPerHost: 1}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			origin := "https://" + raw.Addr().String()
			stream, err := client.Get(origin + "/events")
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Body.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/groups", nil)
			request.Header.Set("Authorization", "Bearer fixture")
			response, err := client.Do(request)
			if !multiplex {
				if !errors.Is(err, context.DeadlineExceeded) || reads.Load() != 0 || stream.ProtoMajor != 1 {
					t.Fatalf("expected connection queue, stream=%s err=%v reads=%d", stream.Proto, err, reads.Load())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.Header.Get("X-Observed-Authorization") != "Bearer fixture" || streamPeer.Load() != readPeer.Load() {
					t.Fatal("HTTP/2 changed peer address or authorization header")
				}
				if stream.ProtoMajor != 2 || response.ProtoMajor != 2 || response.TLS.NegotiatedProtocol != "h2" || reads.Load() != 1 || connections.Load() != 1 {
					t.Fatalf("not multiplexed: stream=%s response=%s reads=%d connections=%d", stream.Proto, response.Proto, reads.Load(), connections.Load())
				}
			}
			_ = stream.Body.Close()
			select {
			case <-streamStopped:
			case <-time.After(time.Second):
				t.Fatal("closing event stream did not cancel handler")
			}
			response, err = client.Get(origin + "/groups")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 || certificateCalls.Load() == 0 {
				t.Fatal("read or original certificate callback failed")
			}
			// A client offering HTTP/1 only must still work on the new listener.
			legacy := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, NextProtos: []string{"http/1.1"}}}
			defer legacy.CloseIdleConnections()
			response, err = (&http.Client{Transport: legacy, Timeout: 5 * time.Second}).Get(origin + "/groups")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.ProtoMajor != 1 {
				t.Fatal("lost HTTP/1 compatibility")
			}
		})
	}
}

func TestHTTPSCertificateFailureDoesNotServeHTTP(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	listener := httpsListener(raw, func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, errors.New("certificate unavailable")
	})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) })}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	// An untrusted certificate configuration cannot reach any application handler.
	client := &http.Client{Timeout: time.Second}
	if response, err := client.Get("https://" + raw.Addr().String()); err == nil {
		_ = response.Body.Close()
		t.Fatal("certificate failure accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("TLS failure reached an HTTP application handler")
	}
}
