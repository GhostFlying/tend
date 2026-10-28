package traex

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestNativeFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		raw, code string
		status    int
	}{
		{`{"codexErrorInfo":{"responseTooManyFailedAttempts":{"httpStatusCode":429}}}`, "rate_limit_exceeded", 429},
		{`{"codexErrorInfo":"rateLimitExceeded"}`, "rate_limit_exceeded", 0},
		{`{"codexErrorInfo":"usageLimitExceeded"}`, "usageLimitExceeded", 0},
		{`{"codexErrorInfo":"unauthorized"}`, "unauthorized", 0},
		{`{"message":"429; retry this request"}`, "unknown", 0},
	} {
		f := nativeFailure(json.RawMessage(tc.raw), false)
		if f.Code != tc.code || f.HTTPStatus != tc.status {
			t.Fatalf("%s: %+v", tc.raw, f)
		}
	}
}

func TestCodexEmptyContinuationAndInputIdentity(t *testing.T) {
	f, s := newFake(t)
	s.codex = true
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "thread"}} })
	f.on(methodTurnStart, func(json.RawMessage) any { return map[string]any{"turn": map[string]any{"id": "turn"}} })
	th, err := s.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, cont := range []bool{false, true} {
		if _, err := th.StartTurn(t.Context(), harness.TurnInput{Continue: cont, Text: "owner input", UserMessageID: "message"}); err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(f.params(methodTurnStart), &wire); err != nil {
			t.Fatal(err)
		}
		if _, ok := wire["userMessageId"]; ok {
			t.Fatal("Codex received TraeX input identity")
		}
		if cont {
			if string(wire["input"]) != "[]" || wire["clientUserMessageId"] != nil {
				t.Fatalf("continuation = %s", f.params(methodTurnStart))
			}
		} else if string(wire["clientUserMessageId"]) != `"message"` {
			t.Fatal(string(f.params(methodTurnStart)))
		}
	}
}

func TestContinuationSafetyRequiresSettledNativeOperations(t *testing.T) {
	for _, tc := range []struct {
		body string
		safe bool
	}{
		{`{"status":"failed","items":[{"type":"dynamicToolCall","status":"completed"}]}`, true},
		{`{"status":"failed","items":[{"type":"commandExecution","status":"completed","exitCode":0}]}`, true},
		{`{"status":"failed","items":[{"type":"commandExecution","status":"completed"}]}`, false},
		{`{"status":"failed","items":[{"type":"dynamicToolCall","status":"inProgress"}]}`, false},
		{`{"status":"failed","items":[{"type":"collabAgentToolCall","status":"completed"}]}`, false},
		{`{"status":"inProgress","items":[]}`, false},
	} {
		if got := continuationSafe(json.RawMessage(tc.body)); got != tc.safe {
			t.Fatalf("%s: %t", tc.body, got)
		}
	}
}
