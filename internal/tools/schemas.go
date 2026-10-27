package tools

import "github.com/GhostFlying/tend/internal/protocol"

// JSON Schemas of the tools' arguments, written by hand next to the
// argument types; TestSchemasMatchArgs keeps the two in step.

const specSchema = `{
  "type": "object",
  "description": "What the task must achieve. Immutable once created; changes make a new version.",
  "properties": {
    "goal": {"type": "string", "description": "The outcome, in the owner's terms."},
    "scope": {"type": "string", "description": "Optional plain-text description of the work boundary, as one string. Use constraints for a list of requirements; keep detailed designs in implementation_context."},
    "implementation_context": {"type":"string","description":"Detailed design or investigation input for implementation only. Never put this material in shared background, scope or acceptance checks to prime an independent reviewer."},
    "shared_context": {"type": "array", "description": "Server-frozen work-item facts. Do not supply on create; the server selects them from linked work items.", "items": {"type": "object", "properties": {"work_item_id": {"type": "string"}, "revision": {"type": "integer"}, "intent": {"type": "string"}, "constraints": {"type": "array", "items": {"type": "string"}}, "facts": {"type": "array", "items": {"type": "object", "properties": {"kind": {"type": "string"}, "text": {"type": "string"}, "source": {"type": "string"}}, "required": ["kind", "text", "source"], "additionalProperties": false}}}, "required": ["work_item_id", "revision", "intent"], "additionalProperties": false}},
    "constraints": {"type": "array", "items": {"type": "string"}},
    "acceptance": {"type": "array", "items": {"type": "string"}, "description": "Checks an independent reviewer applies. Required before work can start."},
    "delivery": {"type": "string", "enum": ["commit"], "description": "Legacy commit-only output. Prefer deliverables for new tasks."},
    "deliverables": {"type": "array", "minItems": 1, "uniqueItems": true, "items": {"type": "string", "enum": ["commit", "report", "files"]}, "description": "Required outputs as an array of names, e.g. [\"report\", \"files\"] for a report and downloadable prototype. Do not use a boolean object. Combine commit/report/files as needed; report does not require Git changes."},
    "inputs": {"type":"array","description":"Explicit original file inputs for both implementation and acceptance. Pass file_id from conversation attachments, submitted artifact files or a prior task input; the server freezes metadata and provenance. Select the needed subset from tasks_read/artifacts_read without requiring re-upload or owner-provided IDs. A submitted artifact need not have passed acceptance. Follow-up relations do not inherit inputs. Binding failures must be corrected, not bypassed by moving IDs into prose.","items":{"type":"object","properties":{"file_id":{"type":"string"},"name":{"type":"string"},"media_type":{"type":"string"},"size":{"type":"integer"},"sha256":{"type":"string"},"source_message_id":{"type":"string"},"source_task_id":{"type":"string","description":"Server-frozen source; callers only need file_id."},"source_artifact_id":{"type":"string","description":"Server-frozen source; callers only need file_id."}},"required":["file_id"],"additionalProperties":false}},
    "observation": {"type":"object","description":"Explicit read-only production Tend facts required by this task and its independent acceptance. Both roles use read_observation. Does not grant owner API, chat, design background, SSH or browser access. Bind this before requiring live internal facts; an unavailable capability leaves the task queued.","properties":{"work_item_ids":{"type":"array","uniqueItems":true,"items":{"type":"string"}},"deployment":{"type":"boolean"}},"additionalProperties":false},
    "max_implement_attempts": {"type": "integer", "minimum": 0},
    "workspace": {
      "type": "object",
      "description": "Choose new repository worktrees, reuse of a same-primary-work-item managed resource identified by source_task_id, or an independent temporary directory for report/file work. A task relation alone does not reuse a workspace.",
      "properties": {
        "mode": {"type": "string", "enum": ["new", "reuse", "temporary"], "description": "Omitted means new. reuse preserves the managed directory and dirty state; temporary is an independent directory, not a repository worktree."},
        "source_task_id": {"type": "string", "description": "For reuse, resolve the owner's referenced task and read its resource first; do not guess an unrelated task or use a relation as implicit inheritance."},
        "include_existing_changes": {"type": "boolean", "description": "Reuse only: explicitly include inherited dirty workspace changes in the task's commit scope. Set only when the owner's requested scope carries those changes forward; otherwise leave false and ask about any blocked inherited changes."},
        "template": {"type": "string"},
        "mounts": {"type": "array", "items": {
          "type": "object",
          "properties": {
            "repo": {"type": "string", "description": "A registered repository; see repos_list."},
            "path": {"type": "string"},
            "base": {"type": "object", "properties": {"branch": {"type": "string"}, "commit": {"type": "string"}}, "additionalProperties": false},
            "profile": {"type": "string"}
          },
          "required": ["repo"],
          "additionalProperties": false
        }},
        "profile": {"type": "string"}
      },
      "additionalProperties": false
    },
    "requirements": {"type": "object", "properties": {"host_id": {"type": "string", "description": "Optional required execution host ID from repos_list. Other hosts cannot claim the task; an offline or busy host leaves it queued."}, "os": {"type": "string"}, "labels": {"type": "array", "items": {"type": "string"}}}, "additionalProperties": false}
  },
  "required": ["goal", "acceptance", "workspace"],
  "anyOf": [{"required": ["delivery"]}, {"required": ["deliverables"]}],
  "additionalProperties": false
}`

const relationsSchema = `{
 "type":"array",
 "description":"Outgoing task references. follow_up continues another task; related records a general reference. References do not inherit code or create scheduling dependencies. On update, replaces outgoing declarations; [] clears them. Backlinks remain owned by their declaring task.",
 "items":{"type":"object","properties":{"kind":{"type":"string","enum":["follow_up","related"]},"task_id":{"type":"string"}},"required":["kind","task_id"],"additionalProperties":false}
}`

var schemas = map[string]string{
	"conversation_turn_adopt":      argumentSchema(protocol.AdoptClarificationPayload{}),
	"conversation_turn_wait":       argumentSchema(protocol.TurnClarification{}),
	"conversation_turn_read":       argumentSchema(turnReadArgs{}),
	"conversation_turn_commit":     argumentSchema(protocol.LogicalTurnCommit{}),
	"background_evidence_read":     argumentSchema(evidenceArgs{}),
	"conversation_associate":       argumentSchema(associateArgs{}),
	"work_item_context_read":       argumentSchema(contextReadArgs{}),
	"work_item_context_prepare":    argumentSchema(contextPrepareArgs{}),
	"work_item_context_fact":       argumentSchema(contextFactArgs{}),
	"agent_conversations_list":     argumentSchema(agentListArgs{}),
	"agent_rollout_read":           argumentSchema(rolloutArgs{}),
	"background_item_read":         argumentSchema(contextReadArgs{}),
	"background_submit":            argumentSchema(protocol.BackgroundAssessmentPayload{}),
	"work_items_refresh_delivery":  argumentSchema(workItemRefreshArgs{}),
	"background_sources":           argumentSchema(backgroundSourceArgs{}),
	"background_checkin":           argumentSchema(backgroundCheckinArgs{}),
	"work_items_followups_list":    argumentSchema(followupListArgs{}),
	"work_items_followups_history": argumentSchema(followupHistoryArgs{}),
	"work_items_followups_create":  argumentSchema(followupCreateArgs{}),
	"work_items_followups_update":  argumentSchema(followupControlArgs{}),
	"work_items_followups_pause":   argumentSchema(followupControlArgs{}),
	"work_items_followups_resume":  argumentSchema(followupControlArgs{}),
	"work_items_followups_cancel":  argumentSchema(followupControlArgs{}),
	"work_items_followups_trigger": argumentSchema(followupControlArgs{}),
	"work_item_choices_list":       argumentSchema(choiceListArgs{}),
	"work_item_choices_create":     argumentSchema(choiceCreateArgs{}),
	"work_item_choices_resolve":    argumentSchema(choiceUpdateArgs{}),
	"work_item_choices_withdraw":   argumentSchema(choiceUpdateArgs{}),
	"work_item_choices_replace":    argumentSchema(choiceUpdateArgs{}),
	"work_items_list":              argumentSchema(workItemsListArgs{}),
	"work_items_read":              argumentSchema(workItemReadArgs{}),
	"work_items_create":            argumentSchema(workItemCreateArgs{}),
	"work_items_update":            argumentSchema(workItemUpdateArgs{}),
	"work_items_close":             argumentSchema(workItemCloseArgs{}),
	"work_items_reopen":            argumentSchema(workItemReopenArgs{}),
	"work_items_link_task":         argumentSchema(workItemLinkArgs{}),
	"tool_failures_list":           argumentSchema(toolFailuresArgs{}),
	"conversation_read":            argumentSchema(conversationArgs{}),
	"tasks_list": `{
  "type": "object",
  "properties": {
    "include_finished": {"type": "boolean", "description": "Also list completed and cancelled tasks."},
    "project": {"type": "string", "description": "List this project's tasks; \"*\" lists every project. Defaults to every project."}
  },
  "additionalProperties": false
}`,
	"tasks_read": `{
  "type": "object",
  "properties": {"task_id": {"type": "string"}},
  "required": ["task_id"],
  "additionalProperties": false
}`,
	"tasks_create": `{
  "type": "object",
  "properties": {
    "work_item_id":{"type":"string","description":"Existing work item to continue. When set, also provide work_item_revision and nonempty stage_ids. Omission creates a standalone concern; use an existing ID for follow-up."},
    "work_item_revision":{"type":"integer","description":"Required with work_item_id: the revision returned by work_items_read/create/update. Creation links atomically under this revision."},
    "stage_ids":{"type":"array","items":{"type":"string"},"description":"Required and nonempty with work_item_id: stable IDs of existing stages this execution covers. Read or create the stages first; do not omit the association to bypass an error."},
    "use_default_workspace":{"type":"boolean","description":"Explicitly reuse the item's verified default resource only to continue its actual work state; independent work uses spec.workspace new even within the same item."},
    "title": {"type": "string"},
    "relations": ` + relationsSchema + `,
    "spec": ` + specSchema + `,
    "rating": {"type": "string", "enum": ["p0", "p1", "p2", "p3"], "description": "How the owner ranks the task: p0 urgent, ahead of everything; p1 high; p2 normal; p3 low. Ask the owner; leave it out if they have not said."},
    "project": {"type": "string", "description": "The project the task belongs to. Required when tend has more than one project."},
    "workspace_name": {"type": "string", "description": "Optional short task alias for hosted calls; required for add-on task identity. For reuse this names the new task only, and does not create or rename its physical resource."},
    "commit_seq": {"type": "integer", "minimum": 1, "description": "1 for the first planning write for this message, then 2, 3 and so on. Repeating a number repeats nothing."},
    "reason": {"type": "string", "maxLength": 500, "description": "One sentence on why, kept with the change."}
  },
  "required": ["title", "spec", "commit_seq"],
  "if": {"required": ["work_item_id"], "properties": {"work_item_id": {"type": "string", "minLength": 1}}},
  "then": {"required": ["work_item_revision", "stage_ids"], "properties": {"stage_ids": {"type": "array", "minItems": 1}}},
  "additionalProperties": false
}`,
	"tasks_update_intent": `{
  "type": "object",
  "properties": {
    "task_id": {"type": "string"},
    "expected_control_revision": {"type": "integer", "description": "The control_revision you last read; stale values are rejected."},
    "relations": ` + relationsSchema + `,
    "spec": ` + specSchema + `,
    "rating": {"type": "string", "enum": ["p0", "p1", "p2", "p3"], "description": "Rate the task: p0 urgent, p1 high, p2 normal, p3 low."},
    "project": {"type": "string", "description": "Move the task to this project."},
    "commit_seq": {"type": "integer", "minimum": 1},
    "reason": {"type": "string", "maxLength": 500, "description": "One sentence on why, kept with the change."}
  },
  "required": ["task_id", "expected_control_revision", "commit_seq"],
  "additionalProperties": false
}`,
	"tasks_set_control": `{
  "type": "object",
  "properties": {
    "task_id": {"type": "string"},
    "expected_control_revision": {"type": "integer", "description": "The control_revision you last read; stale values are rejected."},
    "mode": {"type": "string", "enum": ["paused", "managed"], "description": "paused stops the task; managed hands it back to tend."},
    "reason": {"type": "string", "maxLength": 500, "description": "One sentence on why, kept with the change."}
  },
  "required": ["task_id", "expected_control_revision", "mode"],
  "additionalProperties": false
}`,
	"tasks_cancel": `{
  "type": "object",
  "properties": {
    "task_id": {"type": "string"},
    "expected_control_revision": {"type": "integer", "description": "The control_revision you last read; stale values are rejected."},
    "reason": {"type": "string", "maxLength": 500, "description": "One sentence on why, kept with the change."}
  },
  "required": ["task_id", "expected_control_revision"],
  "additionalProperties": false
}`,
	"actions_create": `{
  "type": "object",
  "description": "Request the next implementation or acceptance after a failure, once the owner decided to retry.",
  "properties": {
    "task_id": {"type": "string"},
    "expected_control_revision": {"type": "integer"},
    "kind": {"type": "string", "enum": ["implement", "accept"]},
    "generation": {"type": "integer", "minimum": 1, "description": "The next generation number shown by tasks_read."},
    "commit_seq": {"type": "integer", "minimum": 1},
    "reason": {"type": "string", "maxLength": 500, "description": "One sentence on why, kept with the change."}
  },
  "required": ["task_id", "expected_control_revision", "kind", "generation", "commit_seq"],
  "additionalProperties": false
}`,
	"decisions_open": `{
  "type": "object",
  "properties": {
    "task_id": {"type": "string"},
    "question": {"type": "string"},
    "options": {"type": "array", "items": {"type": "string"}, "maxItems": 10},
    "reason": {"type": "string", "maxLength": 500, "description": "One sentence on why, kept with the change."}
  },
  "required": ["question", "options"],
  "additionalProperties": false
}`,
	"decisions_resolve": `{
  "type": "object",
  "description": "Record the owner's answer to an open decision.",
  "properties": {
    "decision_id": {"type": "string"},
    "version": {"type": "integer", "description": "The decision version you last read."},
    "option": {"type": "string"},
    "text": {"type": "string"},
    "reason": {"type": "string", "maxLength": 500, "description": "One sentence on why, kept with the change."}
  },
  "required": ["decision_id", "version"],
  "additionalProperties": false
}`,
	"artifacts_read": `{
  "type": "object",
  "properties": {"artifact_id": {"type": "string"}},
  "required": ["artifact_id"],
  "additionalProperties": false
}`,
	"attachments_read": `{
  "type": "object",
  "properties": {
    "file_id": {"type": "string", "description": "Immutable file identity from a finalized artifact manifest."},
    "offset": {"type": "integer", "minimum": 0, "description": "Zero-based byte offset; use next_offset from the last read."},
    "max_bytes": {"type": "integer", "minimum": 4, "maximum": 65536, "description": "Bounded UTF-8 text window, default 16384 bytes. Binary files return metadata and a binary marker without raw content."}
  },
  "required": ["file_id"],
  "additionalProperties": false
}`,
	"files_read": `{
  "type": "object",
  "properties": {
    "file_id": {"type": "string", "description": "Immutable file identity from a finalized artifact manifest."},
    "offset": {"type": "integer", "minimum": 0, "description": "Zero-based byte offset; use next_offset from the last read."},
    "max_bytes": {"type": "integer", "minimum": 4, "maximum": 65536, "description": "Bounded UTF-8 text window, default 16384 bytes. Binary files return metadata and a binary marker without raw content."}
  },
  "required": ["file_id"],
  "additionalProperties": false
}`,
	"requests_read": `{
  "type": "object",
  "properties": {"request_id": {"type": "string"}},
  "required": ["request_id"],
  "additionalProperties": false
}`,
	"repos_list": `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`,
	"tend_context": `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`,
}
