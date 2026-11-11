package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
)

// These cross-component cases run with the race detector and real HTTP/SQLite.
// Keep the injected fault active until the externally observable fact arrives.
func waitBusinessContinuity(t *testing.T, what string, ready func() bool) {
	t.Helper()
	timeout := time.NewTimer(45 * time.Second)
	defer timeout.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-timeout.C:
			t.Fatalf("timed out waiting until %s", what)
		case <-poll.C:
		}
	}
}

type managerEventFailure struct {
	rejected  chan protocol.Envelope
	injected  atomic.Bool
	mu        sync.Mutex
	original  protocol.Envelope
	replayed  chan bool
	replay    sync.Once
	completed bool
}

func (f *managerEventFailure) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	var env protocol.Envelope
	var native protocol.ManagerNativeEvent
	var event coordination.Event
	if json.Unmarshal(body, &env) == nil && env.Operation == protocol.OpManagerNativeEvent &&
		json.Unmarshal(env.Payload, &native) == nil && json.Unmarshal(native.Body, &event) == nil &&
		(!f.completed && event.Call != nil || f.completed && event.Native != nil && event.Native.Kind == harness.EventTurnCompleted) {
		if f.injected.CompareAndSwap(false, true) {
			f.mu.Lock()
			f.original = env
			f.mu.Unlock()
			f.rejected <- env
			failure, _ := json.Marshal(map[string]any{"operation": protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeForbidden, "injected event rejection")}})
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(failure)), Request: req}, nil
		}
		f.mu.Lock()
		original := f.original
		f.mu.Unlock()
		var old protocol.ManagerNativeEvent
		if json.Unmarshal(original.Payload, &old) == nil && env.Target == original.Target && native.Sequence == old.Sequence {
			f.replay.Do(func() {
				f.replayed <- env.RequestID == original.RequestID && bytes.Equal(env.Payload, original.Payload)
			})
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}

type archiveFailureLog struct{ pending chan struct{} }

func (l archiveFailureLog) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("permission denied")) {
		select {
		case l.pending <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

// archiveReceiptLoss accepts the first original upload, drops its reply, and
// holds subsequent archive attempts. All other runner requests still use the
// real authenticated HTTP endpoint.
type archiveReceiptLoss struct {
	mu       sync.Mutex
	original *protocol.Envelope
	changed  bool
	released atomic.Bool
	accepted chan struct{}
	replayed chan struct{}
	replay   sync.Once
}

func (f *archiveReceiptLoss) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	var env protocol.Envelope
	if json.Unmarshal(body, &env) != nil || env.Operation != protocol.OpRunnerRolloutArchive {
		return http.DefaultTransport.RoundTrip(req)
	}
	f.mu.Lock()
	first := f.original == nil
	if first {
		f.original = &env
	} else if env.RequestID != f.original.RequestID || !bytes.Equal(env.Payload, f.original.Payload) {
		f.changed = true
	}
	f.mu.Unlock()
	if first {
		response, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			return response, nil
		}
		_ = response.Body.Close()
		close(f.accepted)
		return nil, errors.New("original archive response lost after server acceptance")
	}
	if !f.released.Load() {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil)), Request: req}, nil
	}
	f.replay.Do(func() { close(f.replayed) })
	return http.DefaultTransport.RoundTrip(req)
}
