package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"strings"
	"testing"
)

func TestFault_ExplicitMissingTitleCorrectionRetainsFailureAndSettles(t *testing.T) {
	for _, kind := range []string{"explicit", "unlinked", "changed_intent", "unknown", "foreign_request"} {
		t.Run(kind, func(t *testing.T) {
			h := hostedManagerHarness(t)
			source, delivery := h.say("Repair the PWA")
			turn := startHostedReview(t, h, "main", delivery)
			p := delegationPayload(source)
			p.ExpectedSourceRevision = rev(1)
			p.Plan.Title = ""
			cmd := CanonicalOwnerIssueCommand{Action: "create"}
			target := protocol.Target{Type: "issue", ID: h.ids.New(protocol.PrefixWorkItem)}
			failed := hostedEffect(t, h, mainAgent, turn, 1, target, nil, p, cmd)
			expectWorkCode(t, failed, protocol.CodeInvalidRequest)
			p.Plan.Title = "Repair the PWA"
			p.CorrectsRequestID = failed.RequestID
			switch kind {
			case "unlinked":
				p.CorrectsRequestID = ""
			case "changed_intent":
				p.Plan.Intent = "Publish an unrelated replacement"
			case "unknown":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE coordination_delegation_attempts SET error_json='{"code":"unknown_outcome"}' WHERE request_id=?`, failed.RequestID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "foreign_request":
				p.CorrectsRequestID = "unrelated-request"
			}
			good := hostedEffect(t, h, mainAgent, turn, 2, target, nil, p, cmd)
			if kind == "explicit" || kind == "unlinked" {
				if good.Status != protocol.OpSucceeded {
					t.Fatal(good)
				}
			} else {
				expectWorkCode(t, good, protocol.CodeInvalidRequest)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
				if err != nil {
					return err
				}
				if (reason == "") != (kind == "explicit") {
					t.Fatal(kind, reason)
				}
				var code string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT json_extract(error_json,'$.code') FROM coordination_delegation_attempts WHERE request_id=?`, failed.RequestID).Scan(&code); err != nil {
					return err
				}
				if kind != "unknown" && code != "invalid_request" {
					t.Fatal("original no-effect failure changed", code)
				}
				if kind == "explicit" {
					var raw string
					if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM events WHERE request_id=? AND kind='conversation.delegation_recorded'`, good.RequestID).Scan(&raw); err != nil {
						return err
					}
					var event map[string]any
					_ = json.Unmarshal([]byte(raw), &event)
					if event["corrects_request_id"] != failed.RequestID {
						t.Fatal(event)
					}
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if kind == "explicit" {
				commitFixtureTurn(t, h, turn.RequestID)
				turn.Status = store.TurnCompleted
				if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
					t.Fatal(err)
				}
				logical, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
				if err != nil || strings.Contains(logical.Reason, "delegation") {
					t.Fatal(logical, err)
				}
			}
		})
	}
}

func TestFault_DelegationCorrectionChainRequiresEveryValidatedEdge(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "validated", false: "rejected_edge"}[valid], func(t *testing.T) {
			h := hostedManagerHarness(t)
			source, delivery := h.say("Repair the PWA")
			turn := startHostedReview(t, h, "main", delivery)
			p := delegationPayload(source)
			p.ExpectedSourceRevision = rev(1)
			p.Plan.Title = ""
			if valid {
				p.Plan.Intent = ""
			}
			cmd := CanonicalOwnerIssueCommand{Action: "create"}
			target := protocol.Target{Type: "issue", ID: h.ids.New(protocol.PrefixWorkItem)}
			first := hostedEffect(t, h, mainAgent, turn, 1, target, nil, p, cmd)
			expectWorkCode(t, first, protocol.CodeInvalidRequest)
			p.CorrectsRequestID = first.RequestID
			if valid {
				p.Plan.Title = "Repair the PWA"
			} else {
				p.Plan.Intent = "Replace the established intent"
			}
			second := hostedEffect(t, h, mainAgent, turn, 2, target, nil, p, cmd)
			expectWorkCode(t, second, protocol.CodeInvalidRequest)
			p.CorrectsRequestID = second.RequestID
			p.Plan.Title = "Repair the PWA"
			if valid {
				p.Plan.Intent = "Repair the PWA without widening scope"
			}
			third := hostedEffect(t, h, mainAgent, turn, 3, target, nil, p, cmd)
			if third.Status != protocol.OpSucceeded {
				t.Fatal(third)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				reason, err := delegationBlockReason(t.Context(), tx.Q(), turn.RequestID)
				if (reason == "") != valid {
					t.Fatal(valid, reason)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
