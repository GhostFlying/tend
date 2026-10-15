package tools

// JSON Schemas of the tools' arguments, written by hand next to the
// argument types; TestSchemasMatchArgs keeps the two in step.

const specSchema = `{
  "type": "object",
  "description": "What the task must achieve. Immutable once created; changes make a new version.",
  "properties": {
    "goal": {"type": "string", "description": "The outcome, in the owner's terms."},
    "scope": {"type": "string"},
    "constraints": {"type": "array", "items": {"type": "string"}},
    "acceptance": {"type": "array", "items": {"type": "string"}, "description": "Checks an independent reviewer applies. Required before work can start."},
    "delivery": {"type": "string", "enum": ["commit"]},
    "max_implement_attempts": {"type": "integer", "minimum": 0},
    "workspace": {
      "type": "object",
      "description": "Repositories to work in. Give either a template name or mounts.",
      "properties": {
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
  "required": ["goal", "acceptance", "delivery", "workspace"],
  "additionalProperties": false
}`

var schemas = map[string]string{
	"tasks_list": `{
  "type": "object",
  "properties": {
    "include_finished": {"type": "boolean", "description": "Also list completed and cancelled tasks."}
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
    "title": {"type": "string"},
    "spec": ` + specSchema + `,
    "priority": {"type": "integer", "description": "Higher runs first. Default 0."},
    "workspace_name": {"type": "string", "description": "Short lowercase name for the workspace and branch, such as fix-windows-build."},
    "commit_seq": {"type": "integer", "minimum": 1, "description": "1 for the first planning write for this message, then 2, 3 and so on. Repeating a number repeats nothing."}
  },
  "required": ["title", "spec", "commit_seq"],
  "additionalProperties": false
}`,
	"tasks_update_intent": `{
  "type": "object",
  "properties": {
    "task_id": {"type": "string"},
    "expected_control_revision": {"type": "integer", "description": "The control_revision you last read; stale values are rejected."},
    "spec": ` + specSchema + `,
    "priority": {"type": "integer"},
    "commit_seq": {"type": "integer", "minimum": 1}
  },
  "required": ["task_id", "expected_control_revision", "commit_seq"],
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
    "commit_seq": {"type": "integer", "minimum": 1}
  },
  "required": ["task_id", "expected_control_revision", "kind", "generation", "commit_seq"],
  "additionalProperties": false
}`,
	"decisions_open": `{
  "type": "object",
  "properties": {
    "task_id": {"type": "string"},
    "question": {"type": "string"},
    "options": {"type": "array", "items": {"type": "string"}, "maxItems": 10}
  },
  "required": ["question", "options"],
  "additionalProperties": false
}`,
	"decisions_resolve": `{
  "type": "object",
  "description": "Record the owner's answer from the current message.",
  "properties": {
    "decision_id": {"type": "string"},
    "version": {"type": "integer", "description": "The decision version you last read."},
    "option": {"type": "string"},
    "text": {"type": "string"}
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
}
