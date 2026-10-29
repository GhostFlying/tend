package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestOwnerSelectionIsExportedWithoutPrivateRoleConfiguration(t *testing.T) {
	raw, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"definitions"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	selection := doc.Definitions["OwnerTaskSpec"].Properties["agent_selection"]
	if !bytes.Contains(selection, []byte("TaskAgentSelection")) {
		t.Fatal("actual owner roots lost the execution selector", string(selection))
	}
	for _, name := range []string{"RunExecutionPin", "AgentExecutor", "TaskAgentSelection"} {
		if len(doc.Definitions[name].Properties) == 0 {
			t.Fatal("actual public root missing", name)
		}
	}
	for _, field := range []string{"model", "effort"} {
		if len(doc.Definitions["AgentRequirement"].Properties[field]) == 0 {
			t.Fatal("actual owner requirement lost profile filter", field)
		}
		for _, required := range doc.Definitions["AgentRequirement"].Required {
			if required == field {
				t.Fatal("optional filter became required", field)
			}
		}
	}
	for _, private := range [][]byte{[]byte(`"instructions"`), []byte(`"executable_ref"`), []byte(`"local_reference"`)} {
		if bytes.Contains(raw, private) {
			t.Fatal("private runtime configuration escaped into owner schema", string(private))
		}
	}
}
