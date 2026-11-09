package tools

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type decisionContinuationReadArgs struct {
	DecisionID string `json:"decision_id"`
}

func decisionContinuationMutation(name string) bool {
	return name == "decision_continuation_answer" || name == "decision_continuation_resume" || name == "decision_continuation_reconcile"
}

func decisionResolveSchema() string {
	var root map[string]any
	if err := json.Unmarshal([]byte(argumentSchema(resolveArgs{})), &root); err != nil {
		panic(err)
	}
	root["description"] = "Legacy answer operation. Conditional worker scheduling must use decision_continuation_answer with an explicit policy."
	props := root["properties"].(map[string]any)
	props["version"].(map[string]any)["description"] = "The original decision version you last read."
	props["reason"].(map[string]any)["maxLength"] = 500
	props["reason"].(map[string]any)["description"] = "One sentence on why, kept with the change."
	out, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return string(out)
}

func decisionContinuationMutationSchema(raw json.RawMessage, name string) json.RawMessage {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		panic(err)
	}
	root["required"] = append(root["required"].([]any), "target_id", "expected_control_revision")
	props := root["properties"].(map[string]any)
	props["target_id"] = map[string]any{"type": "string", "minLength": 1, "description": "Exact original decision_id; never a replacement question or successor Run."}
	props["expected_control_revision"] = map[string]any{"type": "integer", "minimum": 0, "description": "Original parent Task control revision from decision_continuation_read, independent of continuation expected_revision."}
	body := props["payload"].(map[string]any)
	if name == "decision_continuation_answer" {
		body["required"] = append(body["required"].([]any), "source_message_id")
	}
	if name == "decision_continuation_answer" {
		body["required"] = append(body["required"].([]any), "continuation")
	}
	bodyProps := body["properties"].(map[string]any)
	if name == "decision_continuation_answer" {
		bodyProps["version"].(map[string]any)["minimum"] = 1
	} else {
		bodyProps["expected_revision"].(map[string]any)["minimum"] = 0
	}
	bodyProps["source_message_id"] = map[string]any{"type": "string", "minLength": 1, "description": "Exact actual owner source admitted to this execution. Required for owner-controlled or historical waits; a responsible coordinator may omit it only to release a wait explicitly recorded by an Agent. Queued, withdrawn or merely visible messages do not authorize continuation."}
	if name == "decision_continuation_resume" {
		bodyProps["expected_assignment_epoch"].(map[string]any)["description"] = "Optional explicit adoption of this stopped handoff into the current Issue assignment epoch. Read the continuation first; supply its current_assignment_epoch and a reason only when responsibility_adoption_required is true. The frozen Agent and execution pin must remain unchanged."
		bodyProps["expected_assignment_epoch"].(map[string]any)["minimum"] = 1
		bodyProps["reason"].(map[string]any)["description"] = "Why the technical wait can end or the exact stopped responsibility should continue under the current owner. Required when adopting an assignment epoch. This does not authorize merge or deployment."
	}
	out, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return out
}

func (s *Service) readDecisionContinuation(ctx context.Context, execution store.HostedExecution, decisionID string) (protocol.DecisionContinuationView, error) {
	if execution.Selection.Purpose != agentconfig.Main && execution.Selection.Purpose != agentconfig.IssueManager {
		return protocol.DecisionContinuationView{}, protocol.Errorf(protocol.CodeForbidden, "only an admitted coordinator can inspect decision continuation")
	}
	value, err := s.app.ReadDecisionContinuation(ctx, decisionID)
	if err != nil || execution.Selection.Purpose == agentconfig.Main {
		return value, err
	}
	err = s.store.Read(ctx, func(tx *store.ReadTx) error {
		primary, err := store.GetTaskIssueOwner(ctx, tx.Q(), value.TaskID)
		if err != nil {
			return err
		}
		if primary != execution.IssueID {
			return protocol.Errorf(protocol.CodeForbidden, "decision continuation is outside this Issue")
		}
		current, err := store.GetCanonicalIssue(ctx, tx.Q(), primary)
		if err != nil {
			return err
		}
		if current.AssignmentEpoch != execution.AssignmentEpoch || current.AssignmentState != "active" {
			return protocol.Errorf(protocol.CodeForbidden, "Issue responsibility changed")
		}
		return nil
	})
	if err != nil {
		return protocol.DecisionContinuationView{}, err
	}
	return value, nil
}
