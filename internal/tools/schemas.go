package tools

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
    "inputs": {"type":"array","description":"Explicit original attachment inputs for both implementation and acceptance. Pass file_id from conversation attachments or a prior task input; metadata is frozen by the server. Follow-up relations do not inherit inputs.","items":{"type":"object","properties":{"file_id":{"type":"string"},"name":{"type":"string"},"media_type":{"type":"string"},"size":{"type":"integer"},"sha256":{"type":"string"},"source_message_id":{"type":"string"}},"required":["file_id"],"additionalProperties":false}},
    "max_implement_attempts": {"type": "integer", "minimum": 0},
    "workspace": {
      "type": "object",
      "description": "Choose new repository worktrees, reuse of a managed resource identified by source_task_id, or an independent temporary directory for report/file work. A task relation alone does not reuse a workspace.",
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
    "requirements": {"type": "object", "properties": {"os": {"type": "string"}, "labels": {"type": "array", "items": {"type": "string"}}}, "additionalProperties": false}
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
	"work_items_refresh_delivery": argumentSchema(workItemRefreshArgs{}),
	"work_items_list":             argumentSchema(workItemsListArgs{}),
	"work_items_read":             argumentSchema(workItemReadArgs{}),
	"work_items_create":           argumentSchema(workItemCreateArgs{}),
	"work_items_update":           argumentSchema(workItemUpdateArgs{}),
	"work_items_close":            argumentSchema(workItemCloseArgs{}),
	"work_items_reopen":           argumentSchema(workItemReopenArgs{}),
	"work_items_link_task":        argumentSchema(workItemLinkArgs{}),
	"conversation_read": `{
  "type":"object",
  "properties":{
   "before_seq":{"type":"integer","minimum":0,"description":"Read earlier than this sequence; omit to read the latest page. Mutually exclusive with after_seq."},
   "after_seq":{"type":"integer","minimum":0,"description":"Read later than this sequence. Mutually exclusive with before_seq."},
   "limit":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum conversation items; default 20."}
  },
  "additionalProperties":false
 }`,
	"tasks_list": `{
  "type": "object",
  "properties": {
    "include_finished": {"type": "boolean", "description": "Also list completed and cancelled tasks."},
    "project": {"type": "string", "description": "List this project's tasks; \"*\" lists every project. Defaults to the project this session works in."}
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
    "use_default_workspace":{"type":"boolean","description":"Explicitly reuse the item's verified default source task resource; otherwise use spec.workspace."},
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
	"session_project": `{
  "type": "object",
  "properties": {
    "project": {"type": "string", "description": "The project to work in, or an empty string for every project."},
    "reason": {"type": "string", "maxLength": 500, "description": "One sentence on why, kept with the change."}
  },
  "required": ["project"],
  "additionalProperties": false
}`,
}
