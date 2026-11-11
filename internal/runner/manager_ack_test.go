package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestManagerReceiptAcknowledgementSurvivesLostAckAndCompletedRun(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	as := protocol.Assignment{RunID: "run_original", RunToken: "original-token"}
	if err = j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	var sent [][]string
	client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get(headerRun) != as.RunID || req.Header.Get("Authorization") != "Bearer "+as.RunToken {
			t.Fatal("ack changed original run credential")
		}
		if env.Operation == protocol.OpManagerNativeReply {
			return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
		}
		if env.Operation != protocol.OpManagerNativeClaim {
			t.Fatal(env.Operation)
		}
		var p protocol.ManagerNativeClaimPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if !p.AcknowledgeOnly || !reflect.DeepEqual(p.AcknowledgedIDs, []string{"native-command"}) {
			t.Fatal("final ACK could dispatch unrelated work", p)
		}
		sent = append(sent, p.AcknowledgedIDs)
		if len(sent) == 1 {
			return nil, errors.New("server applied ACK but connection lost")
		}
		return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	})}
	r := New(Config{Journal: j, ServerURL: "http://runner.invalid", HTTPClient: client, IDs: testkit.NewIDGen(clock, 3), Now: clock.Now, Log: slog.New(slog.DiscardHandler)})
	r.runnerProtocol = 4
	body, _ := json.Marshal(protocol.ManagerNativeReply{ID: "native-command", Method: "close", Result: json.RawMessage(`null`)})
	env := protocol.Envelope{Operation: protocol.OpManagerNativeReply, RequestID: "original-receipt", Payload: body}
	if err = r.sendManagerReceipt(ctx, as, &env); err != nil {
		t.Fatal("optional compact ACK blocked actual successful receipt", err)
	}
	if ids, err := j.ManagerReceiptAcknowledgements(ctx, as.RunID); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	if err = j.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err = j.Done(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = journal.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	r.cfg.Journal = j
	r.replayManagerAcknowledgements(ctx)
	if ids, err := j.ManagerReceiptAcknowledgements(ctx, as.RunID); err != nil || len(ids) != 0 {
		t.Fatal("delivery watermark not retired after true peer ACK", ids, err)
	}
	if len(sent) != 2 || !reflect.DeepEqual(sent[0], sent[1]) {
		t.Fatal("lost ACK changed original command IDs", sent)
	}
}

func TestManagerAcknowledgementProtocol3AndUnknownRemainConservative(t *testing.T) {
	for _, version := range []int64{3, 4} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			clock := testkit.NewClock()
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "run_original"}
			if err = j.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			calls := 0
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), ServerURL: "http://runner.invalid", RetryDelay: time.Millisecond, RequestTimeout: 20 * time.Millisecond, Log: slog.New(slog.DiscardHandler), HTTPClient: &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				_, _ = io.Copy(io.Discard, req.Body)
				_ = req.Body.Close()
				return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{Status: protocol.OpUnknown}})
			})}})
			r.runnerProtocol = version
			if err = j.AcknowledgeManagerReceipt(t.Context(), as.RunID, protocol.ManagerNativeReply{ID: "old"}); err != nil {
				t.Fatal(err)
			}
			if version == 3 {
				if err = r.flushManagerAcknowledgements(ctx, as); err != nil || calls != 0 {
					t.Fatal("protocol3 falsely confirmed watermark", err, calls)
				}
			}
			env := protocol.Envelope{Operation: protocol.OpManagerNativeReply, RequestID: "original", Payload: json.RawMessage(`{"id":"uncertain","method":"turn","result":null}`)}
			if err = j.PutRequest(t.Context(), journal.Request{RunID: as.RunID, Envelope: env}); err != nil {
				t.Fatal(err)
			}
			if err = r.sendManagerReceipt(ctx, as, &env); err == nil {
				t.Fatal("unknown result treated as success")
			}
			pending, err := j.Requests(t.Context())
			if err != nil || len(pending) != 1 || !reflect.DeepEqual(pending[0].Envelope, env) {
				t.Fatal("unknown original reply changed", pending, err)
			}
			ids, err := j.ManagerReceiptAcknowledgements(t.Context(), as.RunID)
			if err != nil || !reflect.DeepEqual(ids, []string{"old"}) {
				t.Fatal("unknown reply created an ACK", ids, err)
			}
		})
	}
}
