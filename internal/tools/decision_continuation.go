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
	return name == "decision_continuation_resume" || name == "decision_continuation_reconcile"
}

func decisionResolveSchema() string {
	var root map[string]any
	if err := json.Unmarshal([]byte(argumentSchema(resolveArgs{})), &root); err != nil {
		panic(err)
	}
	root["description"] = "Record the owner's actual answer and its explicit worker-handoff continuation, without inferring readiness from text."
	props := root["properties"].(map[string]any)
	props["version"].(map[string]any)["description"] = "The original decision version you last read."
	props["reason"].(map[string]any)["maxLength"] = 500
	props["reason"].(map[string]any)["description"] = "One sentence on why, kept with the change."
	policy := root["definitions"].(map[string]any)["DecisionContinuationPolicy"].(map[string]any)
	policyProps := policy["properties"].(map[string]any)
	policyProps["reason"].(map[string]any)["description"] = "Required nonempty for wait and dependency. Wait requires independent resume; dependency requires exact task/spec completion."
	policyProps["dependency"].(map[string]any)["description"] = "Required only for dependency: one other ordinary execution Task's frozen spec version (manager tasks are rejected), checked against real completed facts."
	out, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return string(out)
}

func decisionContinuationMutationSchema(raw json.RawMessage, resume bool) json.RawMessage {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		panic(err)
	}
	root["required"] = append(root["required"].([]any), "target_id", "expected_control_revision")
	props := root["properties"].(map[string]any)
	props["target_id"] = map[string]any{"type": "string", "minLength": 1, "description": "Exact original decision_id; never a replacement question or successor Run."}
	props["expected_control_revision"] = map[string]any{"type": "integer", "minimum": 0, "description": "Original parent Task control revision from decision_continuation_read, independent of continuation expected_revision."}
	body := props["payload"].(map[string]any)
	if resume {
		body["required"] = append(body["required"].([]any), "source_message_id")
	}
	bodyProps := body["properties"].(map[string]any)
	bodyProps["expected_revision"].(map[string]any)["minimum"] = 0
	bodyProps["source_message_id"] = map[string]any{"type": "string", "minLength": 1, "description": "Exact actual owner source admitted to this execution. A queued, withdrawn or merely visible message cannot authorize continuation."}
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
