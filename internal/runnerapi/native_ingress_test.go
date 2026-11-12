package runnerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func nativeIngressRun(t *testing.T, e *env) protocol.Assignment {
	t.Helper()
	e.createTask()
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "linux-1", protocol.ClaimPayload{}))
	if as == nil {
		t.Fatal("no original run")
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE runs SET role='issue_manager' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	as.Role = protocol.RoleIssueManager
	return *as
}

func TestNativeOriginalHTTPExceedsSmallEnvelopeBudget(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	as := nativeIngressRun(t, e)
	text := strings.Repeat("original tool output ", 260000)
	body, _ := json.Marshal(map[string]any{"thread_id": "original-thread", "native": map[string]any{"Kind": "item_completed", "TurnID": "original-turn", "Tool": map[string]any{"Name": "issues_context_read", "Output": text}, "Raw": map[string]any{"content": text}}})
	original := e.envelope(protocol.OpManagerNativeEvent, as.RunID, protocol.ManagerNativeEvent{Sequence: 1, Body: body})
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked_%v", chunked), func(t *testing.T) {
			// Put payload before operation: staging must preserve all valid field orders.
			raw := []byte(fmt.Sprintf(`{"payload":%s,"target":{"type":"run","id":%q},"operation":%q,"request_id":%q}`, original.Payload, as.RunID, original.Operation, original.RequestID))
			var reader io.Reader = bytes.NewReader(raw)
			if chunked {
				reader = io.NopCloser(reader)
			}
			q, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.server.URL+Path, reader)
			if err != nil {
				t.Fatal(err)
			}
			q.Header.Set("Authorization", "Bearer "+as.RunToken)
			q.Header.Set(HeaderRun, as.RunID)
			resp, err := e.server.Client().Do(q)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var result response
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || result.Operation == nil || result.Operation.Status != protocol.OpSucceeded {
				t.Fatalf("valid original rejected: %d %+v", resp.StatusCode, result)
			}
		})
	}
	events, err := e.app.ManagerNativeEvents(t.Context(), as.RunID, 0)
	if err != nil || len(events) != 1 || !store.SemanticJSONEqual(string(events[0].Body), string(body)) {
		t.Fatalf("original lost or duplicate: count=%d err=%v", len(events), err)
	}
	// A missing original must still block stop. Physical knowledge is not event ACK.
	stop := e.envelope(protocol.OpManagerNativeStopped, as.RunID, protocol.ManagerStoppedPayload{Reason: "actual exit", LastSequence: 2})
	if _, res := e.call(asRun(as), stop); res.Error == nil && (res.Operation == nil || res.Operation.Error == nil) {
		t.Fatal("missing tail treated as stopped")
	}
}

func TestNativeHistoricalLargeReceiptDoesNotGrantOtherOperations(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	as := nativeIngressRun(t, e)
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE leases SET status='released' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"original": strings.Repeat("x", maxBody+100)})
	event := e.envelope(protocol.OpManagerNativeEvent, as.RunID, protocol.ManagerNativeEvent{Sequence: 1, Body: body})
	e.mustOK(asRun(as), event)
	forbidden := e.envelope(protocol.OpRunsSubmitArtifact, as.RunID, map[string]string{"report": strings.Repeat("y", maxBody+100)})
	if status, _ := e.call(asRun(as), forbidden); status != http.StatusForbidden {
		t.Fatalf("historical receipt admitted another operation: %d", status)
	}
	bad := asRun(as)
	bad.token = "wrong"
	if status, _ := e.call(bad, event); status != http.StatusForbidden {
		t.Fatal("invalid token read large body", status)
	}
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM manager_native_events WHERE run_id=?`, as.RunID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("identity changed", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A transport wrapper can be staged while the sole large-original processing
// slot is busy. Cancellation releases only this call's file, not another call's
// budget; ordinary small control requests must remain independently usable.
func TestNativeOriginalBudgetPreservesControlAndCancellation(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	as := nativeIngressRun(t, e)
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	s := New(e.app, e.peers, secret, Config{}, slog.New(slog.DiscardHandler))
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	s.originals <- struct{}{}
	payload := e.envelope(protocol.OpManagerNativeEvent, as.RunID, protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"content":"` + strings.Repeat("x", maxBody+100) + `"}`)})
	raw, _ := json.Marshal(payload)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+Path, bytes.NewReader(raw))
	request.Header.Set(HeaderRun, as.RunID)
	request.Header.Set("Authorization", "Bearer "+as.RunToken)
	done := make(chan error, 1)
	go func() {
		resp, err := server.Client().Do(request)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		files, _ := os.ReadDir(temp)
		if len(files) > 0 {
			info, err := files[0].Info()
			if err == nil && info.Size() == int64(len(raw)) {
				break
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("original was not staged")
		case <-time.After(time.Millisecond):
		}
	}
	// A small event is a real control-path write, with no provider call.
	oldURL := e.server
	e.server = server
	e.mustOK(asRun(as), e.envelope(protocol.OpManagerNativeEvent, as.RunID, protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"disconnected":true}`)}))
	e.server = oldURL
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("budget wait ignored cancellation")
	}
	// Wait for the handler to acknowledge cancellation and unlink its private file.
	deadline2 := time.NewTimer(time.Second)
	defer deadline2.Stop()
	for {
		files, _ := os.ReadDir(temp)
		if len(files) == 0 {
			break
		}
		select {
		case <-deadline2.C:
			t.Fatal("cancelled original staging leaked")
		case <-time.After(time.Millisecond):
		}
	}
	if len(s.originals) != 1 {
		t.Fatal("cancellation released another original's budget")
	}
	<-s.originals
	events, err := e.app.ManagerNativeEvents(t.Context(), as.RunID, 0)
	if err != nil || len(events) != 1 || !strings.Contains(string(events[0].Body), "disconnected") {
		t.Fatal("cancelled body created an event", err)
	}
}

func TestNativeOriginalFailedStreamNeverAcknowledgesOrLeavesStaging(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	as := nativeIngressRun(t, e)
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	s := New(e.app, e.peers, secret, Config{}, slog.New(slog.DiscardHandler))
	request := httptest.NewRequest(http.MethodPost, Path, &failedOriginalBody{})
	request.ContentLength = -1
	request.Header.Set(HeaderRun, as.RunID)
	request.Header.Set("Authorization", "Bearer "+as.RunToken)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code < 400 {
		t.Fatal("partial original acknowledged")
	}
	files, _ := os.ReadDir(temp)
	if len(files) != 0 {
		t.Fatal("partial original retained", len(files))
	}
	events, err := e.app.ManagerNativeEvents(t.Context(), as.RunID, 0)
	if err != nil || len(events) != 0 {
		t.Fatal("partial original published", err)
	}
}

type failedOriginalBody struct{}

func (*failedOriginalBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// Chunked transfer encoding does not turn a small control operation into a
// retained original. A busy original decoder cannot hold this control write.
func TestNativeSmallChunkedControlDoesNotWaitForOriginalBudget(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	as := nativeIngressRun(t, e)
	s := New(e.app, e.peers, secret, Config{}, slog.New(slog.DiscardHandler))
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	s.originals <- struct{}{}
	defer func() { <-s.originals }()
	env := e.envelope(protocol.OpManagerNativeEvent, as.RunID, protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"disconnected":true}`)})
	raw, _ := json.Marshal(env)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	q, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+Path, io.NopCloser(bytes.NewReader(raw)))
	q.Header.Set(HeaderRun, as.RunID)
	q.Header.Set("Authorization", "Bearer "+as.RunToken)
	resp, err := server.Client().Do(q)
	if err != nil {
		t.Fatalf("small chunked control waited for original budget: %v", err)
	}
	defer resp.Body.Close()
	var result response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || result.Operation == nil || result.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("small control rejected: %d %+v", resp.StatusCode, result)
	}
	events, err := e.app.ManagerNativeEvents(t.Context(), as.RunID, 0)
	if err != nil || len(events) != 1 || !bytes.Equal(events[0].Body, json.RawMessage(`{"disconnected":true}`)) {
		t.Fatalf("control receipt missing: %+v %v", events, err)
	}
	if len(s.originals) != 1 {
		t.Fatal("small control consumed another original's budget")
	}
}

func TestNativeSmallChunkedHistoricalMessageRetainsExactReceiptAuthentication(t *testing.T) {
	e, as, m, _ := httpMessageRun(t)
	e.mustOK(asRun(as), e.envelope(protocol.OpRunsSubmitArtifact, as.RunID, protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{{Repo: "tend", Path: "tend", BaseSHA: baseSHA, Branch: as.Workspace.Branch, CommitSHA: commitSHA, Changed: true}}}))
	before := receiptExecutionSnapshot(t, e, as)
	receipt := protocol.RunMessageReceipt{RunMessageDelivery: m.RunMessageDelivery, Status: protocol.RunMessageAccepted}
	env := e.envelope(protocol.OpRunsReportMessage, as.RunID, receipt)
	raw, _ := json.Marshal(env)
	for attempt := 0; attempt < 2; attempt++ {
		q, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.server.URL+Path, io.NopCloser(bytes.NewReader(raw)))
		q.Header.Set(HeaderRun, as.RunID)
		q.Header.Set("Authorization", "Bearer "+as.RunToken)
		resp, err := e.server.Client().Do(q)
		if err != nil {
			t.Fatal(err)
		}
		var result response
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || result.Operation == nil || result.Operation.Status != protocol.OpSucceeded {
			t.Fatalf("exact nonmanager historical receipt rejected: status=%d result=%+v err=%v", resp.StatusCode, result, err)
		}
	}
	page, err := e.app.RunMessages(t.Context(), receiptAgent, as.RunID, 0, 100, false)
	if err != nil || page.Messages[0].Status != protocol.RunMessageAccepted || len(page.Messages[0].History) != 4 {
		t.Fatalf("original receipt not reconciled exactly once: %+v %v", page, err)
	}
	if after := receiptExecutionSnapshot(t, e, as); !reflect.DeepEqual(before, after) {
		t.Fatal("historical receipt changed task, run or released lease")
	}
}

func TestEnvelopeDecodeErrorPreservesIOFailure(t *testing.T) {
	failure := errors.New("original stream read failed")
	var env protocol.Envelope
	err := json.NewDecoder(errorOriginalBody{failure}).Decode(&env)
	if got := envelopeDecodeError(t.Context(), err); !errors.Is(got, failure) {
		t.Fatalf("IO failure became permanent JSON rejection: %v", got)
	}
	for _, raw := range []string{"", `{"request_id":`, `{"request_id":}`, `{"request_id":1}`} {
		err := json.NewDecoder(strings.NewReader(raw)).Decode(&env)
		if got := envelopeDecodeError(t.Context(), err); protocol.CodeOf(got) != protocol.CodeInvalidRequest {
			t.Fatalf("invalid JSON %q not classified: %v", raw, got)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := envelopeDecodeError(ctx, failure); !errors.Is(got, context.Canceled) {
		t.Fatalf("cancellation lost: %v", got)
	}
}

type errorOriginalBody struct{ err error }

func (b errorOriginalBody) Read([]byte) (int, error) { return 0, b.err }

func TestNativeUnknownLengthRejectsBeforeUnauthenticatedRemainder(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	as := nativeIngressRun(t, e)
	s := New(e.app, e.peers, secret, Config{}, slog.New(slog.DiscardHandler))
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked_%v", chunked), func(t *testing.T) {
			body := &countedOriginalBody{Reader: strings.NewReader(strings.Repeat("x", maxBody+4096))}
			request := httptest.NewRequest(http.MethodPost, Path, body)
			request.ContentLength = maxBody + 4096
			if chunked {
				request.ContentLength = -1
			}
			request.Header.Set(HeaderRun, as.RunID)
			request.Header.Set("Authorization", "Bearer wrong-token")
			result := httptest.NewRecorder()
			s.Handler().ServeHTTP(result, request)
			if result.Code != http.StatusForbidden {
				t.Fatalf("invalid credential admitted: %d", result.Code)
			}
			expected := 0
			if chunked {
				expected = maxBody + 1
			}
			if body.read != expected {
				t.Fatalf("read %d unauthenticated bytes, want bounded prefix %d", body.read, expected)
			}
		})
	}
}

func TestNativeEnvelopeStreamIOIsNotPermanentPayloadRejection(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	as := nativeIngressRun(t, e)
	s := New(e.app, e.peers, secret, Config{}, slog.New(slog.DiscardHandler))
	failure := errors.New("transport stream failed")
	for _, length := range []int64{1, -1} {
		request := httptest.NewRequest(http.MethodPost, Path, errorOriginalBody{failure})
		request.ContentLength = length
		request.Header.Set(HeaderRun, as.RunID)
		request.Header.Set("Authorization", "Bearer "+as.RunToken)
		result := httptest.NewRecorder()
		s.Handler().ServeHTTP(result, request)
		var decoded response
		if err := json.Unmarshal(result.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		if result.Code != http.StatusInternalServerError || decoded.Error == nil || decoded.Error.Code != protocol.CodeInternal {
			t.Fatalf("transport failure became permanent payload rejection: %d %+v", result.Code, decoded)
		}
		if strings.Contains(result.Body.String(), failure.Error()) {
			t.Fatal("internal IO cause exposed over protocol")
		}
	}
	events, err := e.app.ManagerNativeEvents(t.Context(), as.RunID, 0)
	if err != nil || len(events) != 0 {
		t.Fatal("failed stream acknowledged", err)
	}
}

type countedOriginalBody struct {
	io.Reader
	read int
}

func (b *countedOriginalBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
