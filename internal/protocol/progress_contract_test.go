package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol/schema"
)

func TestProgressAndWorkspaceInputSchema(t *testing.T) {
	g := schema.New()
	g.Add(UpdateIssueProgressPayload{})
	g.Add(OwnerTaskBase{})
	raw, err := g.Document("input")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Definitions map[string]map[string]any }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	progress := doc.Definitions["UpdateIssueProgressPayload"]
	constraints, ok := progress["allOf"].([]any)
	if !ok || len(constraints) != 1 {
		t.Fatal("progress character limits absent", progress)
	}
	fields := constraints[0].(map[string]any)["properties"].(map[string]any)
	for field, limit := range map[string]float64{"summary": 500, "next_step": 240} {
		property := fields[field].(map[string]any)
		if property["type"] != "string" || property["maxLength"] != limit {
			t.Fatal(field, property)
		}
	}
	if doc.Definitions["OwnerTaskBase"]["not"] == nil {
		t.Fatal("base branch/commit exclusivity absent")
	}
}

func TestProgressInputLimitsCountUnicodeCharacters(t *testing.T) {
	for _, tc := range []struct {
		name, summary, next, field string
	}{
		{"boundary", strings.Repeat("界", 500), strings.Repeat("🙂", 240), ""},
		{"clear", "", "", ""},
		{"summary", strings.Repeat("界", 501), "continue", "summary"},
		{"next_step", "done", strings.Repeat("🙂", 241), "next_step"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := UpdateIssueProgressPayload{Summary: tc.summary, NextStep: tc.next}
			err := p.ValidateInput()
			if tc.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			pe, ok := err.(*Error)
			if !ok || pe.Code != CodeInvalidRequest || !strings.Contains(pe.Message, tc.field) {
				t.Fatal("missing actionable field", err)
			}
			var details struct {
				Field       string `json:"field"`
				Unit        string `json:"unit"`
				Max, Actual int
			}
			if err := json.Unmarshal(pe.Details, &details); err != nil || details.Field != tc.field || details.Unit != "unicode_code_points" || details.Actual != details.Max+1 {
				t.Fatal("incorrect character diagnostics", string(pe.Details), err)
			}
		})
	}
}
