package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// prepareBusinessEnvelope allocates actual object identities only while a typed
// command composes new input. Saved input is validated with allocation disabled.
func prepareBusinessEnvelope(e *Env, env *protocol.Envelope, allocate bool) error {
	if prefix := protocol.CreationTargetPrefix(env.Operation); prefix != "" {
		if env.Target.ID == "" && allocate {
			env.Target.ID = e.IDs.New(prefix)
		}
		if !protocol.ValidID(prefix, env.Target.ID) {
			return usagef("%s requires the original target.id (%s) before submission; use --id or save the first --dry-run", env.Operation, prefix)
		}
	}
	identities := protocol.NativePayloadIdentities(env.Operation)
	if len(identities) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &fields); err != nil || fields == nil {
		return usagef("%s payload must be a JSON object", env.Operation)
	}
	names := make([]string, 0, len(identities))
	for name := range identities {
		names = append(names, name)
	}
	sort.Strings(names)
	changed := false
	for _, name := range names {
		prefix := identities[name]
		var id string
		if raw, present := fields[name]; present && json.Unmarshal(raw, &id) != nil {
			return usagef("payload.%s must be a string", name)
		}
		if id == "" && allocate {
			id = e.IDs.New(prefix)
			fields[name], _ = json.Marshal(id)
			changed = true
		}
		if !protocol.ValidID(prefix, id) {
			return usagef("%s requires the original payload.%s (%s) before submission", env.Operation, name, prefix)
		}
	}
	if changed {
		var err error
		env.Payload, err = json.Marshal(fields)
		return err
	}
	return nil
}

func validateBusinessEnvelope(env protocol.Envelope) error {
	if err := env.Validate(); err != nil {
		return err
	}
	return prepareBusinessEnvelope(nil, &env, false)
}

// businessRecovery points at live business state, never a request-result cache.
func businessRecovery(env protocol.Envelope) string {
	id := env.Target.ID
	command := "status --json"
	var payload struct {
		MessageID      string `json:"message_id"`
		TaskID         string `json:"task_id"`
		RuleID         string `json:"rule_id"`
		SubscriptionID string `json:"subscription_id"`
		Release        struct {
			ID string `json:"id"`
		} `json:"release"`
	}
	_ = json.Unmarshal(env.Payload, &payload)
	switch env.Target.Type {
	case "issue", "work_item":
		command = "issue show " + id + " --json"
	case "task":
		command = "task show " + id + " --json"
	case "run":
		command = "run maintenance-reconciliation " + id + " --json"
	case "decision":
		command = "decision-continuation show " + id + " --json"
	case "repo":
		command = "repo show " + id + " --json"
	case "attention":
		command = "attention show " + id + " --json"
	case "extensions":
		command = "extensions status --json"
	case "host":
		command = "host show " + id + " --json"
	case "upgrade":
		command = "upgrade show " + id + " --json"
	case "category":
		command = "category show " + id + " --json"
	case "concurrency", "agent_concurrency_group", "concurrency_group":
		command = "agents concurrency show " + id + " --json"
	case "conversation":
		command = "conversation info " + id + " --json"
	case "message":
		if payload.MessageID != "" {
			id = payload.MessageID
		}
		if strings.HasPrefix(env.Operation, "conversation.") {
			command = "message original " + id + " --json"
		} else {
			command = "message show " + id + " --json"
		}
	case "usage_source":
		command = "usage collection show " + id + " --json"
	case "followup":
		command = "schedule show " + id + " --json"
	case "deadline":
		command = "issue deadline show " + id + " --json"
	case "planner_entry":
		command = "next-action show " + id + " --json"
	case "logical_turn":
		command = "conversation queue --json"
	}
	switch env.Operation {
	case protocol.OpRunsSendMessage:
		command = "run messages " + env.Target.ID + " --json"
	case "conversation.inject_source":
		command = "source show " + payload.MessageID + " --json"
	case "conversation.handoff":
		command = "conversation handoff --status --json"
	case "issues.create_task":
		command = "task show " + payload.TaskID + " --json"
	case "issues.followups_create", "work_items.followups_create":
		command = "issue followup-history " + env.Target.ID + " " + payload.RuleID + " --json"
	case "webhooks.create":
		command = "webhook show " + payload.SubscriptionID + " --json"
	case "extensions.publish", "extensions.rollback":
		if payload.Release.ID != "" {
			command = "extensions releases " + payload.Release.ID + " --json"
		}
	}
	return fmt.Sprintf("inspect tend %s for actual business state; retain object IDs, original payload and control values; do not resubmit an unknown external action", command)
}
