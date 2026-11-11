package cli

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestTypedCreationFixesBusinessIdentityBeforeSubmission(t *testing.T) {
	for _, args := range [][]string{
		{"issue", "create", "--data", `{}`},
		{"attention", "create", "--data", `{}`},
		{"conversation", "consult", "--data", `{"title":"Independent investigation"}`},
		{"conversation", "send", "conversation", "--data", `{"body":"hello"}`},
		{"source", "inject", "conversation", "--data", `{"text":"source"}`},
		{"run", "message", "run", "--revision", "2", "--data", `{"task_id":"task","text":"hello"}`},
		{"message", "send", "--data", `{"body":"hello"}`},
		{"message", "reply", "original-message", "--data", `{"body":"reply"}`},
		{"message", "notify", "--data", `{"body":"progress","purpose":"progress","reason":"Observe actual work"}`},
		{"issue", "create-task", "issue", "--revision", "2", "--epoch", "3", "--data", `{}`},
		{"issue", "followup", "create", "issue", "--revision", "2", "--epoch", "3", "--data", `{}`},
		{"schedule", "create", "--issue", "issue", "--revision", "2", "--epoch", "3", "--data", `{}`},
		{"issue", "deadline", "put", "--data", `{}`},
		{"next-action", "put", "--data", `{}`},
		{"webhook", "create", "--issue", "issue", "--revision", "2", "--epoch", "3", "--data", `{}`},
	} {
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			if code := e.main(append(slices.Clone(args), "--dry-run")...); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
			if len(f.requests) != 0 {
				t.Fatal("composition contacted server")
			}
			saved := slices.Clone(e.out.Bytes())
			var original protocol.Envelope
			if err := json.Unmarshal(saved, &original); err != nil {
				t.Fatal(err)
			}
			if prefix := protocol.CreationTargetPrefix(original.Operation); prefix != "" && !protocol.ValidID(prefix, original.Target.ID) {
				t.Fatal("new target missing", original)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original.Payload, &fields); err != nil {
				t.Fatal(err)
			}
			for field, prefix := range protocol.NativePayloadIdentities(original.Operation) {
				var id string
				if json.Unmarshal(fields[field], &id) != nil || !protocol.ValidID(prefix, id) {
					t.Fatalf("missing nested identity %s: %s", field, original.Payload)
				}
			}
			f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, raw []byte) {
				var sent protocol.Envelope
				if json.Unmarshal(raw, &sent) != nil || sent.Target != original.Target || !sameCapabilityPayload(t, &sent, &original) || sent.RequestID != original.RequestID {
					t.Error("saved input changed", string(raw), string(saved))
				}
				_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: original.RequestID, Status: protocol.OpSucceeded}})
			})
			if code := e.main("operation", "submit", "--data", string(saved)); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
			if len(f.seen("POST /api/v1/ops")) != 1 {
				t.Fatal("submission repeated")
			}
		})
	}
}

func TestSavedOperationRejectsMissingBusinessIdentitiesWithoutIO(t *testing.T) {
	for _, operation := range []string{"issues.create", "attentions.create", "conversation.consult", "issues.create_task", "issues.followups_create", "conversation.submit_message", "conversation.inject_source", "messaging.send", "messaging.reply", "messaging.notify", "runs.send_message", "issues.deadlines_put", "planner.entries_put", "webhooks.create"} {
		t.Run(operation, func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			env := protocol.Envelope{RequestID: "req_01ARZ3NDEKTSV4RRFFQ69G5FAV", Operation: operation, Target: protocol.Target{Type: "issue"}, Payload: json.RawMessage(`{}`)}
			raw, _ := json.Marshal(env)
			for _, dry := range [][]string{nil, {"--dry-run"}} {
				if code := e.main(append([]string{"operation", "submit", "--data", string(raw)}, dry...)...); code != exitUsage {
					t.Fatal("saved input silently acquired an identity", code, e.errOut.String())
				}
			}
			if len(f.requests) != 0 {
				t.Fatal("invalid saved input contacted server")
			}
		})
	}
}

func TestExplicitCreationIdentitySurvivesTypedComposition(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	id := "wi_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if code := e.main("issue", "create", "--id", id, "--request-id", "req_01ARZ3NDEKTSV4RRFFQ69G5FAV", "--data", `{}`, "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var env protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &env); err != nil || env.Target.ID != id {
		t.Fatal(env, err)
	}
	if code := e.main("issue", "create", "--request-id", env.RequestID, "--data", `{}`, "--dry-run"); code != exitUsage {
		t.Fatal("retained trace allocated a different Issue", code, e.errOut.String())
	}
	if len(f.requests) != 0 {
		t.Fatal("preview performed IO")
	}
}

func TestRetiredRequestCommandsDoNotReadOrWrite(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	for _, args := range [][]string{{"request", "show", "original"}, {"request", "replay", "--data", `{}`}, {"issue", "request", "original"}, {"issue", "replay", "--data", `{}`}, {"extensions", "reconcile", "original"}} {
		if code := e.main(args...); code != exitUsage {
			t.Fatal("retired command still enabled", args, code, e.errOut.String())
		}
	}
	if len(f.requests) != 0 {
		t.Fatal("retired request interface performed IO")
	}
}

func TestSavedOperationHelpUsesBusinessStateAndPreservesCompleteInput(t *testing.T) {
	e := newTestEnv(t, "")
	if code := e.main("operation", "submit", "--help"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	for _, term := range []string{"Target.ID", "message_id", "task_id", "rule_id", "original revisions", "neither looks up a request result nor automatically retries"} {
		if !strings.Contains(e.out.String(), term) {
			t.Error("missing recovery semantics", term)
		}
	}
	f := newFakeServer(t)
	e = newTestEnv(t, f.srv.URL)
	for _, input := range []string{
		`{"request_id":"req_01ARZ3NDEKTSV4RRFFQ69G5FAV","operation":"issues.pause","target":{"type":"issue","id":"original"},"unexpected":"must not disappear","payload":{}}`,
		`{"request_id":"req_01ARZ3NDEKTSV4RRFFQ69G5FAV","operation":"issues.pause","target":{"type":"issue","id":"original"},"payload":{}} {}`,
	} {
		if code := e.main("operation", "submit", "--data", input); code != exitUsage {
			t.Fatal("saved input was silently changed", code, e.errOut.String())
		}
	}
	if len(f.requests) != 0 {
		t.Fatal("malformed saved input performed IO")
	}
}

func TestUnknownRecoveryNamesActualNestedBusinessObject(t *testing.T) {
	for _, test := range []struct{ operation, kind, payload, read string }{
		{"issues.create_task", "issue", `{"task_id":"task_original"}`, "task show task_original"},
		{"issues.followups_create", "issue", `{"rule_id":"followup_original"}`, "issue followup-history parent followup_original"},
		{"webhooks.create", "issue", `{"subscription_id":"webhook_original"}`, "webhook show webhook_original"},
		{"conversation.inject_source", "message", `{"message_id":"msg_original"}`, "source show msg_original"},
		{protocol.OpRunsSendMessage, "run", `{"message_id":"msg_original"}`, "run messages parent"},
		{"extensions.publish", "extensions", `{"release":{"id":"release-original"}}`, "extensions releases release-original"},
		{"concurrency.update", "concurrency", `{}`, "agents concurrency show parent"},
	} {
		guidance := businessRecovery(protocol.Envelope{Operation: test.operation, Target: protocol.Target{Type: test.kind, ID: "parent"}, Payload: json.RawMessage(test.payload)})
		if !strings.Contains(guidance, test.read) || strings.Contains(guidance, "request show") {
			t.Fatal("recovery did not name business state", test.operation, guidance)
		}
	}
}

func TestWebhookMissingRetainedBusinessIdentityDoesNotStageCredentials(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	e.Stdin = strings.NewReader(strings.Repeat("a", 32))
	if code := e.main("webhook", "create", "--issue", "original-issue", "--revision", "2", "--epoch", "3", "--request-id", "req_01ARZ3NDEKTSV4RRFFQ69G5FAV", "--data", `{}`, "--secret-file", "-"); code != exitUsage {
		t.Fatal("retained input acquired another webhook", code, e.errOut.String())
	}
	if len(f.requests) != 0 {
		t.Fatal("missing business identity staged credentials or submitted a mutation")
	}
}

func TestExplicitParentAndPlannerCreationKeysDoNotRefreshCAS(t *testing.T) {
	for _, args := range [][]string{
		{"next-action", "put", "--id", "next_01ARZ3NDEKTSV4RRFFQ69G5FAV", "--data", `{}`},
		{"issue", "deadline", "put", "--id", "deadline_01ARZ3NDEKTSV4RRFFQ69G5FAV", "--data", `{}`},
		{"schedule", "create", "--issue", "original-issue", "--id", "followup_01ARZ3NDEKTSV4RRFFQ69G5FAV", "--revision", "2", "--epoch", "3", "--data", `{}`},
		{"webhook", "create", "--issue", "original-issue", "--id", "webhook_01ARZ3NDEKTSV4RRFFQ69G5FAV", "--revision", "2", "--epoch", "3", "--data", `{}`},
	} {
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			call := append(slices.Clone(args), "--request-id", "req_01ARZ3NDEKTSV4RRFFQ69G5FAV", "--dry-run")
			if code := e.main(call...); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
			var env protocol.Envelope
			if err := json.Unmarshal(e.out.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			if prefix := protocol.CreationTargetPrefix(env.Operation); prefix != "" && (env.Target.ID == "" || env.ExpectedControlRevision != nil) {
				t.Fatal("creation acquired an update CAS", env)
			}
			if len(f.requests) != 0 {
				t.Fatal("creation refreshed current state")
			}
		})
	}
}
