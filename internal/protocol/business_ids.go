package protocol

// CreationTargetPrefix names objects whose creation uses Target.ID as the
// business key. Parent-scoped commands carry their own key in the payload.
func CreationTargetPrefix(operation string) Prefix {
	switch operation {
	case "planner.entries_put":
		return Prefix("next")
	case "issues.deadlines_put":
		return Prefix("deadline")
	case "tasks.create":
		return PrefixTask
	case "issues.create", "work_items.create":
		return PrefixWorkItem
	case "decisions.open":
		return PrefixDecision
	case "attentions.create", "attentions.raise":
		return Prefix("attn")
	case "conversation.consult":
		return Prefix("consultation:conversation")
	default:
		return ""
	}
}

// NativePayloadIdentities names caller-allocated business objects nested under
// an existing target. Callers allocate these once, before submitting the action.
func NativePayloadIdentities(operation string) map[string]Prefix {
	switch operation {
	case OpRunsReportTakeover:
		return map[string]Prefix{"observation_id": PrefixRequest}
	case "webhooks.create":
		return map[string]Prefix{"subscription_id": Prefix("webhook")}
	case "issues.create_task":
		return map[string]Prefix{"task_id": PrefixTask}
	case "issues.followups_create", "work_items.followups_create":
		return map[string]Prefix{"rule_id": Prefix("followup")}
	case "conversation.submit_message", "conversation.inject_source", "messaging.input", "messaging.send", "messaging.reply", "messaging.notify", "messaging.respond", "conversation.reply", "conversation.publish", "conversation.notify", "runs.send_message":
		return map[string]Prefix{"message_id": PrefixMessage}
	default:
		return nil
	}
}
