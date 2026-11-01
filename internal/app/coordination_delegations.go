package app

import (
	"context"
	"crypto/sha256"
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
	if _, err := tx.Exec(ctx, `INSERT INTO coordination_delegation_attempts(domain,request_id,execution_id,logical_turn_id,operation,source_message_id,target_id,intent_key,succeeded,error_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, x.Principal.DedupDomain(), x.Env.RequestID, c.ExecutionID, logical, c.Operation(), source, x.Env.Target.ID, intent, failure == nil, errorJSON, x.Now.UnixMilli()); err != nil {
		return false, err
	}
	body, _ := json.Marshal(map[string]any{"logical_turn_id": logical, "source_message_id": source, "operation": c.Operation(), "succeeded": failure == nil, "error": failure})
	actor, _ := json.Marshal(x.Principal)
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
	raw := x.Env.Payload
	fallback := fmt.Sprintf("%x", sha256.Sum256(raw))
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
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func delegationBlockReason(ctx context.Context, q store.Querier, logical string) (string, error) {
	var failed int
	// A correction resolves only the same source and business target. Invalid or
	// omitted sources remain explicit failures; another message cannot hide them.
	err := q.QueryRowContext(ctx, `WITH RECURSIVE responsibility(id) AS (
 SELECT ? UNION SELECT l.logical_turn_id FROM logical_turns l JOIN responsibility r ON l.resumed_by=r.id)
 SELECT COUNT(*) FROM coordination_delegation_attempts f WHERE f.logical_turn_id IN responsibility AND f.succeeded=0 AND NOT EXISTS (
 SELECT 1 FROM coordination_delegation_attempts s WHERE s.logical_turn_id IN responsibility AND s.source_message_id=f.source_message_id AND s.operation=f.operation AND s.target_id=f.target_id AND s.intent_key=f.intent_key AND s.succeeded=1 AND s.seq>f.seq)`, logical).Scan(&failed)
	if err != nil || failed == 0 {
		return "", err
	}
	return fmt.Sprintf("%d delegation attempts remain unresolved; review the original failed requests before continuing business work", failed), nil
}
