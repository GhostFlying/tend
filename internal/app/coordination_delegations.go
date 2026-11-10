package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func delegationCommand(cmd Command) (HostedCoordinationCommand, bool) {
	c, ok := cmd.(HostedCoordinationCommand)
	return c, ok && (c.Operation() == "issues.create" || (c.Operation() == protocol.OpIssueRequestManager || c.Operation() == protocol.OpIssueRequestOwner))
}

// recordDelegationAttempt runs outside the Apply savepoint but in the same
// transaction as its request receipt. Authorization failures never reach here.
func recordDelegationAttempt(ctx context.Context, x *Exec, tx *store.Tx, cmd Command, failure *protocol.Error) (bool, error) {
	c, ok := delegationCommand(cmd)
	if !ok {
		return false, nil
	}
	execution, err := store.GetHostedExecution(ctx, tx.Q(), c.ExecutionID)
	if err != nil {
		return false, err
	}
	if execution.Selection.Purpose != agentconfig.Main {
		return false, nil
	}
	var logical string
	if err := tx.Q().QueryRowContext(ctx, `SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?`, c.ExecutionID).Scan(&logical); err != nil {
		return false, err
	}
	source := hostedPayloadSource(x.Env.Payload)
	intent := delegationIntent(ctx, x, tx.Q(), c.Operation())
	intentJSON := delegationCanonical(ctx, x, tx.Q(), c.Operation())
	correction := x.validatedDelegationCorrection
	if failure != nil && failure.Code == protocol.CodeDuplicateEffect {
		var details struct {
			Original string `json:"original_request_id"`
		}
		_ = json.Unmarshal(failure.Details, &details)
		// Older successful receipts predate the attempt journal. Exact payload
		// identity still proves that their business effect is already recorded.
		original, err := store.GetRequest(ctx, tx.Q(), x.Principal.DedupDomain(), details.Original)
		if err == nil && (original.Status == protocol.OpSucceeded || original.Status == protocol.OpAccepted) {
			hash, err := protocol.PayloadHash(x.Env)
			if err != nil {
				return false, err
			}
			if hash == original.PayloadHash {
				return false, nil
			}
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
		var same bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM coordination_delegation_attempts WHERE domain=? AND request_id=? AND operation=? AND source_message_id=? AND target_id=? AND intent_key=? AND succeeded=1)`, x.Principal.DedupDomain(), details.Original, c.Operation(), source, x.Env.Target.ID, intent).Scan(&same); err != nil {
			return false, err
		}
		if same {
			return false, nil
		}
	}
	errorJSON := ""
	if failure != nil {
		encoded, err := json.Marshal(failure)
		if err != nil {
			return false, err
		}
		errorJSON = string(encoded)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO coordination_delegation_attempts(domain,request_id,execution_id,logical_turn_id,operation,source_message_id,target_id,intent_key,succeeded,error_json,created_at,intent_json,corrects_request_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, x.Principal.DedupDomain(), x.Env.RequestID, c.ExecutionID, logical, c.Operation(), source, x.Env.Target.ID, intent, failure == nil, errorJSON, x.Now.UnixMilli(), string(intentJSON), correction); err != nil {
		return false, err
	}
	body, _ := json.Marshal(map[string]any{"logical_turn_id": logical, "source_message_id": source, "operation": c.Operation(), "succeeded": failure == nil, "error": failure, "corrects_request_id": correction})
	actor, _ := json.Marshal(x.ActualPrincipal())
	turn, err := store.GetMainTurn(ctx, tx.Q(), c.ExecutionID)
	if err != nil {
		return false, err
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: turn.MessageID, Kind: "conversation.delegation_recorded", BodyJSON: string(body), RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: string(actor), CreatedAt: x.Now.UnixMilli()})
	return true, err
}

// The intended contract stays fixed while source/CAS fields are corrected.
// Creation has no target ID, so source alone cannot distinguish two Issues.
func delegationIntent(ctx context.Context, x *Exec, q store.Querier, operation string) string {
	return fmt.Sprintf("%x", sha256.Sum256(delegationCanonical(ctx, x, q, operation)))
}

func delegationCanonical(ctx context.Context, x *Exec, q store.Querier, operation string) []byte {
	raw := x.Env.Payload
	fallback := raw
	var canonical any
	switch operation {
	case "issues.create":
		var p protocol.CreateIssuePayload
		if json.Unmarshal(raw, &p) != nil {
			return fallback
		}
		if p.Plan.Stages == nil {
			p.Plan.Stages = []protocol.IssueStage{}
		}
		if project, err := resolveProject(ctx, q, p.Plan.Project, x.Principal); err == nil {
			p.Plan.Project = project.Project
		}
		slices.Sort(p.DependsOn)
		if p.Owner != nil {
			owner := *p.Owner
			p.Owner = &owner
			p.Owner.Reason = ""
		}
		if p.Owner == nil && p.Manager == nil && p.Assignee == nil {
			p.Manager = &protocol.RequestIssueManagerPayload{}
		}
		if p.Manager != nil {
			if p.Manager.CategoryID == "" {
				p.Manager.CategoryID = protocol.DefaultIssueManagerCategory
			}
			if p.Manager.SourceMessageID == "" || p.Manager.SourceMessageID == p.SourceMessageID {
				p.Manager.SourceMessageID = ""
			}
			p.Manager.ExpectedAssignmentEpoch = 0
			p.Manager.Reason = ""
		}
		p.CorrectsRequestID = ""
		p.SourceMessageID = ""
		p.ExpectedSourceRevision = nil
		p.ExpectedParentRevision = nil
		canonical = p
	case protocol.OpIssueRequestManager, protocol.OpIssueRequestOwner:
		var p protocol.RequestIssueManagerPayload
		if json.Unmarshal(raw, &p) != nil {
			return fallback
		}
		if p.CategoryID == "" {
			p.CategoryID = protocol.DefaultIssueManagerCategory
		}
		p.CorrectsRequestID = ""
		p.SourceMessageID = ""
		p.ExpectedAssignmentEpoch = 0
		p.Reason = ""
		canonical = p
	default:
		return fallback
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return fallback
	}
	return encoded
}

func delegationBlockReason(ctx context.Context, q store.Querier, logical string) (string, error) {
	var failed int
	// Validated correction chains resolve only the same source and business target. Invalid or
	// omitted sources remain explicit failures; another message cannot hide them.
	err := q.QueryRowContext(ctx, `WITH RECURSIVE responsibility(id) AS (
 SELECT ? UNION SELECT l.logical_turn_id FROM logical_turns l JOIN responsibility r ON l.resumed_by=r.id),
 corrections(domain,request_id,root_domain,root_request_id) AS (
 SELECT domain,request_id,domain,corrects_request_id FROM coordination_delegation_attempts WHERE corrects_request_id<>'' AND logical_turn_id IN responsibility
 UNION
 SELECT s.domain,s.request_id,c.root_domain,c.root_request_id FROM coordination_delegation_attempts s JOIN corrections c ON s.domain=c.domain AND s.corrects_request_id=c.request_id WHERE s.logical_turn_id IN responsibility)
 SELECT COUNT(*) FROM coordination_delegation_attempts f WHERE f.logical_turn_id IN responsibility AND f.succeeded=0 AND NOT EXISTS (
 SELECT 1 FROM coordination_delegation_attempts s WHERE s.logical_turn_id IN responsibility AND s.source_message_id=f.source_message_id AND s.operation=f.operation AND s.target_id=f.target_id AND (s.intent_key=f.intent_key OR (EXISTS(SELECT 1 FROM corrections c WHERE c.domain=s.domain AND c.request_id=s.request_id AND c.root_domain=f.domain AND c.root_request_id=f.request_id))) AND s.succeeded=1 AND s.seq>f.seq)`, logical).Scan(&failed)
	if err != nil || failed == 0 {
		return "", err
	}
	return fmt.Sprintf("%d delegation attempts remain unresolved; review the original failed requests before continuing business work", failed), nil
}

// inheritedDelegationCorrectionAdmitted preserves an exact, rejected delegation
// across a real clarification handoff. Adoption does not grant general source
// authority, and uncertain original requests cannot be retried as corrections.
func inheritedDelegationCorrectionAdmitted(ctx context.Context, x *Exec, q store.Querier, execution, source, operation string) (bool, error) {
	if operation != "issues.create" && operation != protocol.OpIssueRequestManager && operation != protocol.OpIssueRequestOwner {
		return false, nil
	}
	if source != hostedPayloadSource(x.Env.Payload) {
		return false, nil
	}
	intent := delegationIntent(ctx, x, q, operation)
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE responsibility(id) AS (
 SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?
 UNION SELECT l.logical_turn_id FROM logical_turns l JOIN responsibility p ON l.resumed_by=p.id WHERE l.status='awaiting_completion')
 SELECT DISTINCT f.execution_id FROM coordination_delegation_attempts f
 JOIN request_records r ON r.domain=f.domain AND r.request_id=f.request_id
 WHERE f.logical_turn_id IN responsibility AND f.logical_turn_id<>(SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?)
 AND f.domain=? AND f.operation=? AND f.target_id=? AND f.source_message_id=? AND f.intent_key=? AND f.succeeded=0
 AND r.operation=f.operation AND r.status='failed' AND json_valid(f.error_json) AND json_extract(f.error_json,'$.code') IN ('invalid_request','stale_revision')
 AND NOT EXISTS(SELECT 1 FROM coordination_delegation_attempts s WHERE s.logical_turn_id IN responsibility AND s.source_message_id=f.source_message_id AND s.operation=f.operation AND s.target_id=f.target_id AND s.intent_key=f.intent_key AND s.succeeded=1 AND s.seq>f.seq)`, execution, execution, x.Principal.DedupDomain(), operation, x.Env.Target.ID, source, intent)
	if err != nil {
		return false, err
	}
	var originals []string
	for rows.Next() {
		var original string
		if err := rows.Scan(&original); err != nil {
			rows.Close()
			return false, err
		}
		originals = append(originals, original)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	for _, original := range originals {
		admitted, err := mainExecutionSourceAdmitted(ctx, q, original, source)
		if err != nil || admitted {
			return admitted, err
		}
	}
	return false, nil
}

// Explicit corrections may fill missing contract text, but cannot replace an
// established intention or authorize unknown/rejected-source effects.
func delegationCorrection(raw []byte) string {
	var p struct {
		Corrects string `json:"corrects_request_id"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Corrects
}

func validateDelegationCorrection(ctx context.Context, x *Exec, q store.Querier, logical, operation string) error {
	original := delegationCorrection(x.Env.Payload)
	if original == "" {
		return nil
	}
	var before, source, target, op, status, code, oldExecution, inheritedCorrection string
	err := q.QueryRowContext(ctx, `WITH RECURSIVE responsibility(id) AS (
 SELECT ? UNION SELECT l.logical_turn_id FROM logical_turns l JOIN responsibility r ON l.resumed_by=r.id)
 SELECT f.intent_json,f.source_message_id,f.target_id,f.operation,r.status,r.error_code,f.execution_id,f.corrects_request_id
 FROM coordination_delegation_attempts f JOIN request_records r ON r.domain=f.domain AND r.request_id=f.request_id
 WHERE f.domain=? AND f.request_id=? AND f.succeeded=0 AND f.logical_turn_id IN responsibility`, logical, x.Principal.DedupDomain(), original).Scan(&before, &source, &target, &op, &status, &code, &oldExecution, &inheritedCorrection)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "correction requires a failed delegation in this responsibility")
	}
	if err != nil {
		return err
	}
	if status != "failed" || (code != "invalid_request" && code != "stale_revision") || source == "" || source != hostedPayloadSource(x.Env.Payload) || target != x.Env.Target.ID || op != operation || before == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "correction does not prove the original admitted, no-effect failure")
	}
	admitted, err := mainExecutionSourceAdmitted(ctx, q, oldExecution, source)
	if err != nil {
		return err
	}
	if !admitted && inheritedCorrection == "" {
		return protocol.Errorf(protocol.CodeForbidden, "original correction source was not admitted")
	}
	after := delegationCanonical(ctx, x, q, operation)
	var prior, next map[string]any
	if json.Unmarshal([]byte(before), &prior) != nil || json.Unmarshal(after, &next) != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "correction intent is unavailable")
	}
	// Source/CAS fixes keep the same canonical intent. Missing title/intent may
	// also be completed; established values and every other contract field stay fixed.
	if operation == "issues.create" {
		p, pok := prior["plan"].(map[string]any)
		n, nok := next["plan"].(map[string]any)
		if pok && nok {
			for _, field := range []string{"title", "intent"} {
				if p[field] == "" {
					p[field] = n[field]
				}
			}
		}
	}
	a, _ := json.Marshal(prior)
	b, _ := json.Marshal(next)
	if string(a) != string(b) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "correction changes established delegation intent")
	}
	return nil
}
