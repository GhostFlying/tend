package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestManagerReceiptBackoffBoundsRequestsAndPreservesEnvelope(t *testing.T) {
	for _, failure := range []string{"http_500_unknown", "http_409_unknown", "transport"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				env := protocol.Envelope{RequestID: "original-reply", TraceID: "original-trace", Operation: protocol.OpManagerNativeReply, Target: protocol.Target{Type: "run", ID: "original-run"}, Payload: json.RawMessage(`{"id":"original-close","last_sequence":19}`)}
				original, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				var attempts atomic.Int64
				client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
					defer req.Body.Close()
					raw, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					if !bytes.Equal(raw, original) || req.Header.Get(headerRun) != "original-run" || req.Header.Get("Authorization") != "Bearer original-token" {
						t.Error("receipt retry changed its original envelope or identity")
					}
					attempts.Add(1)
					if attempts.Load() <= 4 {
						if failure == "transport" {
							return nil, errors.New("connection lost")
						}
						status := http.StatusInternalServerError
						if failure == "http_409_unknown" {
							status = http.StatusConflict
						}
						return managerReceiptHTTPResponse(status, rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "native close must cover all applied events")})
					}
					return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
				})}
				r := New(Config{ServerURL: "http://runner.invalid", HTTPClient: client, Now: time.Now, Log: slog.New(slog.DiscardHandler)})
				done := make(chan error, 1)
				go func() {
					done <- r.sendManagerReceipt(t.Context(), protocol.Assignment{RunID: "original-run", RunToken: "original-token"}, &env)
				}()
				synctest.Wait()
				if attempts.Load() != 1 {
					t.Fatalf("initial attempts = %d, want 1", attempts.Load())
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				if attempts.Load() != 2 {
					t.Fatalf("short receipt race lost its initial retry: attempts = %d", attempts.Load())
				}
				time.Sleep(27 * time.Second)
				synctest.Wait()
				if attempts.Load() != 4 {
					t.Fatalf("unconfirmed receipt was retried too often: attempts = %d at 29s, want 4", attempts.Load())
				}
				time.Sleep(time.Second)
				if err := <-done; err != nil || attempts.Load() != 5 {
					t.Fatalf("receipt did not recover within its retry bound: attempts = %d, error = %v", attempts.Load(), err)
				}
			})
		})
	}
}

func TestManagerReceiptBackoffCancellationAndLongConfiguredDelay(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "long_delay", true: "cancel_wait"}[cancelWait], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var attempts atomic.Int64
				client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
					_ = req.Body.Close()
					attempts.Add(1)
					if attempts.Load() == 1 {
						return managerReceiptHTTPResponse(http.StatusInternalServerError, rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "pending events")})
					}
					return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
				})}
				r := New(Config{ServerURL: "http://runner.invalid", HTTPClient: client, Now: time.Now, RetryDelay: 2 * time.Minute, Log: slog.New(slog.DiscardHandler)})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					done <- r.sendManagerReceipt(ctx, protocol.Assignment{RunID: "run"}, &protocol.Envelope{Operation: protocol.OpManagerNativeReply})
				}()
				synctest.Wait()
				time.Sleep(59 * time.Second)
				synctest.Wait()
				if attempts.Load() != 1 {
					t.Fatalf("explicit retry delay was shortened: attempts = %d", attempts.Load())
				}
				if cancelWait {
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) || attempts.Load() != 1 {
						t.Fatalf("cancelled receipt retried: attempts = %d, error = %v", attempts.Load(), err)
					}
					return
				}
				time.Sleep(time.Second)
				if err := <-done; !errors.Is(err, context.DeadlineExceeded) || attempts.Load() != 1 {
					t.Fatalf("configured retry delay exceeded the total receipt budget: attempts = %d, error = %v", attempts.Load(), err)
				}
			})
		})
	}
}

func TestManagerReceiptReturnsPermanentRejection(t *testing.T) {
	for _, failedOperation := range []bool{false, true} {
		t.Run(map[bool]string{false: "rpc_error", true: "failed_operation"}[failedOperation], func(t *testing.T) {
			var attempts atomic.Int64
			rejected := protocol.Errorf(protocol.CodeConflictPayload, "original payload differs")
			client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
				_ = req.Body.Close()
				attempts.Add(1)
				if failedOperation {
					return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{Status: protocol.OpFailed, Error: rejected}})
				}
				return managerReceiptHTTPResponse(http.StatusConflict, rpcResponse{Error: rejected})
			})}
			r := New(Config{ServerURL: "http://runner.invalid", HTTPClient: client, Now: time.Now, Log: slog.New(slog.DiscardHandler)})
			err := r.sendManagerReceipt(t.Context(), protocol.Assignment{RunID: "run"}, &protocol.Envelope{Operation: protocol.OpManagerNativeReply})
			if protocol.CodeOf(err) != protocol.CodeConflictPayload || attempts.Load() != 1 {
				t.Fatalf("permanent rejection retried: attempts = %d, error = %v", attempts.Load(), err)
			}
		})
	}
}

func managerReceiptHTTPResponse(status int, response rpcResponse) (*http.Response, error) {
	body, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
}
