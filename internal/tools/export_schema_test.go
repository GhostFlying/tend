package tools

import (
	"encoding/json"
	"testing"
)

func TestExportedPlanningSchemaRetainsNestedInputContracts(t *testing.T) {
	for _, tool := range Specs() {
		if tool.Name != "tasks_create" && tool.Name != "work_items_create" {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if tool.Name == "tasks_create" {
			file := planningSchemaAt(t, schema, "spec", "inputs", "[]", "file_id")
			if file["type"] != "string" {
				t.Fatal("file identity type lost", file)
			}
			inputs := planningSchemaAt(t, schema, "spec", "inputs", "[]")
			if _, exists := inputs["properties"].(map[string]any)["url"]; exists {
				t.Fatal("file inputs advertise URL input")
			}
		} else {
			message := planningSchemaAt(t, schema, "initial_context", "source_reviews", "[]", "message_id")
			if message["type"] != "string" {
				t.Fatal("initial source review type lost")
			}
			evidence := planningSchemaAt(t, schema, "plan", "stages", "[]", "evidence", "[]")
			if props := evidence["properties"].(map[string]any); props["observation"] != nil || props["upgrade"] != nil {
				t.Fatal("model-facing evidence contains server-frozen snapshots")
			}
			var original map[string]any
			if err := json.Unmarshal([]byte(schemas[tool.Name]), &original); err != nil {
				t.Fatal(err)
			}
			frozen := original["definitions"].(map[string]any)["IssueEvidence"].(map[string]any)["properties"].(map[string]any)
			if frozen["observation"] == nil || frozen["upgrade"] == nil {
				t.Fatal("model export changed the runtime validation schema")
			}

		}
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if _, ok := x["$ref"]; ok {
					t.Fatal("unexpanded tool reference", x)
				}
				for _, v := range x {
					walk(v)
				}
			case []any:
				for _, v := range x {
					walk(v)
				}
			}
		}
		walk(schema)
	}
}

func TestPlanningSchemasFitNativeCompactionBudget(t *testing.T) {
	var strip func(any) any
	strip = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, x := range v {
				if k != "description" && k != "title" {
					out[k] = strip(x)
				}
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, x := range v {
				out[i] = strip(x)
			}
			return out
		default:
			return value
		}
	}
	for _, tool := range Specs() {
		switch tool.Name {
		case "tasks_create", "tasks_update_intent", "work_items_create", "work_items_update", "work_items_close":
		default:
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(strip(schema))
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > 5000 {
			t.Errorf("%s: %d bytes would trigger native deep-schema compaction", tool.Name, len(body))
		}
	}
}
