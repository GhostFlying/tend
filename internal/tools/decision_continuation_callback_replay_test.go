package tools

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDecisionResolveLegacyCallbackPreservesOriginalPayloadAndReceipt(t *testing.T) {
	for _, answer := range []string{"option-only", "text-only"} {
		for _, explicitID := range []bool{false, true} {
			name := answer + "/native-id"
			if explicitID {
				name = answer + "/explicit-id"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "msg_1", Author: "owner", Body: "Use the original answer"})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				opened := e.call("decisions_open", openArgs{Question: "Which scope?", Options: []string{"bounded"}}, "open-original")
				var created struct {
					Outcome struct {
						DecisionID string `json:"decision_id"`
					} `json:"outcome"`
				}
				if !opened.OK || json.Unmarshal(opened.Result, &created) != nil || created.Outcome.DecisionID == "" {
					t.Fatal(opened)
				}
				args := resolveArgs{DecisionID: created.Outcome.DecisionID, Version: 1}
				if answer == "option-only" {
					args.Option = "bounded"
				} else {
					args.Text = "The original owner answer"
				}
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				call := Call{Principal: hosted, Native: "th_1/tu_1/original-answer", SourceMessageID: "msg_1", Tool: "decisions_resolve", Arguments: raw}
				if explicitID {
					call.RequestID = "req_01ARZ3NDEKTSV4RRFFQ69G5FAV"
				}
				cmd := app.AnswerDecision{FromMessage: true}
				// This is the exact committed legacy handler shape, including empty keys.
				legacyPayload := map[string]any{"version": args.Version, "option": args.Option, "text": args.Text, "source_message_id": call.SourceMessageID}
				first, err := e.svc.execute(t.Context(), call, cmd.Operation(), protocol.Target{Type: "decision", ID: args.DecisionID}, nil, args.Reason, legacyPayload, cmd)
				if err != nil {
					t.Fatal("original valid callback failed", err)
				}
				originalID := first["request_id"].(string)
				result := e.svc.Handle(t.Context(), call)
				var replay answerView
				if err := json.Unmarshal([]byte(result.Text), &replay); err != nil || !replay.OK {
					t.Fatal("upgraded callback lost original receipt", replay, err)
				}
				var got any
				if err := json.Unmarshal(replay.Result, &got); err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(first)
				var want any
				json.Unmarshal(encoded, &want)
				if !reflect.DeepEqual(got, want) {
					t.Fatal("original receipt changed", got, want)
				}
				if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
					d, err := store.GetDecision(t.Context(), tx.Q(), args.DecisionID)
					if err == nil && d.Status != store.DecisionAnswered {
						t.Fatal("answer was not committed", d)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if !explicitID {
					id, err := e.svc.app.NativeRequestID(t.Context(), "harness:"+call.Principal.Subject, call.Native)
					if err != nil || id != originalID {
						t.Fatal("original native mapping changed", id, err)
					}
				}
			})
		}
	}
}
