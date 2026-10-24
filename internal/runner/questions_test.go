package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func questionRunner(t *testing.T, server *httptest.Server) *ownerQuestions {
	t.Helper()
	clock := testkit.NewClock()
	r := New(Config{ServerURL: server.URL, HTTPClient: server.Client(), Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), RetryDelay: time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return &ownerQuestions{r: r, as: protocol.Assignment{RunID: "run_question", RunToken: "run-only", WorkerQuestions: true}, prepared: Prepared{StateDir: t.TempDir()}}
}

func TestOwnerQuestionIdentitySurvivesLostReceiptAndRunnerRestart(t *testing.T) {
	payload := protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Include legacy input?", Evidence: "The investigation found two supported formats.", Options: []string{"yes", "no"}}
	question := protocol.WorkerQuestion{DecisionID: "decision_owner", QuestionKey: payload.QuestionKey, Question: payload.Question, Evidence: payload.Evidence, Status: "open"}
	var answered atomic.Bool
	waiting := make(chan struct{}, 1)
	var mu sync.Mutex
	var envelopes []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get(headerRun) != "run_question" || req.Header.Get("Authorization") != "Bearer run-only" {
			t.Error("question did not use its run identity")
		}
		current := question
		if answered.Load() {
			current.Status, current.Answer = "answered", json.RawMessage(`{"choice":"yes"}`)
		}
		if req.Method == http.MethodPost {
			var env protocol.Envelope
			if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
				t.Error(err)
			}
			mu.Lock()
			envelopes = append(envelopes, env)
			count := len(envelopes)
			mu.Unlock()
			if count == 1 {
				_, _ = w.Write([]byte("lost receipt"))
				return
			}
			body, _ := json.Marshal(current)
			_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: body}})
			return
		}
		if req.URL.Query().Get("wait") != "25" || req.URL.Path != "/runner/v1/questions/decision_owner" {
			t.Error("expected bounded question wait")
		}
		waiting <- struct{}{}
		<-req.Context().Done()
	}))
	defer server.Close()
	owner := questionRunner(t, server)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := owner.ask(ctx, payload); done <- err }()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("question never waited")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled question leaked")
	}
	answered.Store(true)
	// The native tool ID and callback instance need not survive. The original
	// durable question identity does, and its answer is replayable.
	restarted := &ownerQuestions{r: owner.r, as: owner.as, prepared: owner.prepared}
	for range 2 {
		got, err := restarted.ask(t.Context(), payload)
		if err != nil || got.Status != "answered" || string(got.Answer) != `{"choice":"yes"}` {
			t.Fatalf("replayed answer: %+v %v", got, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(envelopes) != 4 {
		t.Fatalf("requests: %d", len(envelopes))
	}
	for _, env := range envelopes {
		if env.RequestID != envelopes[0].RequestID || !bytes.Equal(env.Payload, envelopes[0].Payload) {
			t.Fatal("question retry changed immutable identity")
		}
	}
	payload.Question = "Different question"
	if _, err := restarted.ask(t.Context(), payload); err == nil {
		t.Fatal("changed payload reused existing key")
	}
}

func TestQuestionRecoveryFinishesUnsentIntentBeforeContinuing(t *testing.T) {
	var creates int
	var createdID string
	question := protocol.WorkerQuestion{DecisionID: "decision_pending", QuestionKey: "scope", Question: "Which scope?", Evidence: "Investigation evidence", Status: "open"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]protocol.WorkerQuestion{})
			return
		}
		creates++
		var env protocol.Envelope
		_ = json.NewDecoder(req.Body).Decode(&env)
		createdID = env.RequestID
		body, _ := json.Marshal(question)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: body}})
	}))
	defer server.Close()
	owner := questionRunner(t, server)
	env, err := owner.envelope(protocol.AskOwnerPayload{QuestionKey: "scope", Question: question.Question, Evidence: question.Evidence})
	if err != nil {
		t.Fatal(err)
	}
	restarted := &ownerQuestions{r: owner.r, as: owner.as, prepared: owner.prepared}
	questions, err := restarted.recover(t.Context())
	if err != nil || len(questions) != 1 || questions[0].DecisionID != question.DecisionID || creates != 1 || createdID != env.RequestID {
		t.Fatalf("recovery lost pending question: %+v %d %v", questions, creates, err)
	}
}

func TestQuestionRejectsInactiveAnswerAndRequiresNegotiation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		body, _ := json.Marshal(protocol.WorkerQuestion{DecisionID: "old", Status: "cancelled", Answer: json.RawMessage(`{"choice":"yes"}`)})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: body}})
	}))
	defer server.Close()
	owner := questionRunner(t, server)
	payload := protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Continue?", Evidence: "Scope changed"}
	if _, err := owner.ask(t.Context(), payload); err == nil {
		t.Fatal("stale answer authorized work")
	}
	owner.as.WorkerQuestions = false
	if _, err := owner.ask(t.Context(), payload); err == nil || calls != 1 {
		t.Fatal("question sent to unnegotiated server")
	}
	owner.as.WorkerQuestions = true
	owner.prepared.StateDir = filepath.Join(owner.prepared.StateDir, "owner-questions", "not-a-directory")
	// Existing envelope handling must never depend on native call IDs. A fresh
	// key still gets its own durable request identity.
	payload.QuestionKey = "next_scope"
	if env, err := owner.envelope(payload); err != nil || env.RequestID == "" {
		t.Fatalf("new question identity: %v", err)
	}
}

func TestRejectedQuestionDoesNotPoisonRecovery(t *testing.T) {
	for _, loseReceipt := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "crash_before_receipt"}[loseReceipt], func(t *testing.T) {
			var requests []protocol.Envelope
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode([]protocol.WorkerQuestion{})
					return
				}
				var env protocol.Envelope
				_ = json.NewDecoder(req.Body).Decode(&env)
				requests = append(requests, env)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeInvalidRequest, "options must be distinct")}})
			}))
			defer server.Close()
			owner := questionRunner(t, server)
			payload := protocol.AskOwnerPayload{QuestionKey: "invalid", Question: "Scope?", Evidence: "evidence", Options: []string{"same", "same"}}
			if _, err := owner.ask(t.Context(), payload); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
				t.Fatalf("expected definitive rejection: %v", err)
			}
			env, err := owner.envelope(payload)
			if err != nil {
				t.Fatal(err)
			}
			if loseReceipt {
				if err := os.Remove(owner.rejectionPath(env)); err != nil {
					t.Fatal(err)
				}
			}
			restarted := &ownerQuestions{r: owner.r, as: owner.as, prepared: owner.prepared}
			for range 2 {
				if got, err := restarted.recover(t.Context()); err != nil || len(got) != 0 {
					t.Fatalf("rejected question prevents restart: %+v %v", got, err)
				}
			}
			want := 1
			if loseReceipt {
				want = 2
			}
			if len(requests) != want {
				t.Fatalf("terminal rejection replayed: %d requests, want %d", len(requests), want)
			}
			if _, err := restarted.ask(t.Context(), payload); protocol.CodeOf(err) != protocol.CodeInvalidRequest || len(requests) != want {
				t.Fatalf("saved rejection was not replayed: %v", err)
			}
			preserved, err := restarted.envelope(payload)
			if err != nil || preserved.RequestID != env.RequestID || !bytes.Equal(preserved.Payload, env.Payload) {
				t.Fatal("rejection destroyed original request evidence")
			}
			for _, got := range requests {
				if got.RequestID != env.RequestID || !bytes.Equal(got.Payload, env.Payload) {
					t.Fatal("recovery replaced rejected request identity")
				}
			}
		})
	}
}

func TestQuestionUnknownOutcomeRemainsRecoverableUnderOriginalIdentity(t *testing.T) {
	var requests []protocol.Envelope
	var unknown = true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]protocol.WorkerQuestion{})
			return
		}
		var env protocol.Envelope
		_ = json.NewDecoder(req.Body).Decode(&env)
		requests = append(requests, env)
		if unknown {
			_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpUnknown, Error: protocol.Errorf(protocol.CodeUnknownOutcome, "receipt unavailable")}})
			return
		}
		body, _ := json.Marshal(protocol.WorkerQuestion{DecisionID: "decision_recovered", QuestionKey: "scope", Status: "open"})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: body}})
	}))
	defer server.Close()
	owner := questionRunner(t, server)
	payload := protocol.AskOwnerPayload{QuestionKey: "scope", Question: "Scope?", Evidence: "evidence"}
	if _, err := owner.ask(t.Context(), payload); err == nil {
		t.Fatal("unknown was treated as an answer")
	}
	env, err := owner.envelope(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owner.rejectionPath(env)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown marked as terminal rejection: %v", err)
	}
	unknown = false
	restarted := &ownerQuestions{r: owner.r, as: owner.as, prepared: owner.prepared}
	questions, err := restarted.recover(t.Context())
	if err != nil || len(questions) != 1 || questions[0].DecisionID != "decision_recovered" {
		t.Fatalf("unknown intent lost: %+v %v", questions, err)
	}
	if len(requests) != 2 || requests[0].RequestID != requests[1].RequestID || !bytes.Equal(requests[0].Payload, requests[1].Payload) {
		t.Fatal("unknown request identity changed on recovery")
	}
}
