package protocol

import (
	"encoding/json"
	"unicode/utf8"
)

const (
	MaxIssueProgressSummaryCharacters  = 500
	MaxIssueProgressNextStepCharacters = 240
)

// ValidateInput preserves the existing Unicode character limits across inputs.
func (p UpdateIssueProgressPayload) ValidateInput() error {
	for _, field := range []struct {
		name, value string
		limit       int
	}{
		{"summary", p.Summary, MaxIssueProgressSummaryCharacters},
		{"next_step", p.NextStep, MaxIssueProgressNextStepCharacters},
	} {
		if !utf8.ValidString(field.value) {
			return Errorf(CodeInvalidRequest, "%s must be valid UTF-8", field.name)
		}
		actual := utf8.RuneCountInString(field.value)
		if actual > field.limit {
			details, _ := json.Marshal(map[string]any{"field": field.name, "max": field.limit, "actual": actual, "unit": "unicode_code_points"})
			pe := Errorf(CodeInvalidRequest, "%s exceeds %d Unicode characters (got %d)", field.name, field.limit, actual)
			pe.Details = details
			return pe
		}
	}
	return nil
}

func (UpdateIssueProgressPayload) JSONSchemaConstraints() map[string]any {
	return map[string]any{"allOf": []any{map[string]any{"properties": map[string]any{
		"summary":   map[string]any{"type": "string", "maxLength": MaxIssueProgressSummaryCharacters},
		"next_step": map[string]any{"type": "string", "maxLength": MaxIssueProgressNextStepCharacters},
	}}}}
}

func (OwnerTaskBase) JSONSchemaConstraints() map[string]any {
	// Empty values historically mean unspecified. Reject only two nonempty
	// selectors, matching workspace validation rather than field presence alone.
	return map[string]any{
		"description": "Choose a base branch or a base commit, never both. Empty values mean unspecified.",
		"not": map[string]any{"required": []string{"branch", "commit"}, "properties": map[string]any{
			"branch": map[string]any{"type": "string", "minLength": 1},
			"commit": map[string]any{"type": "string", "minLength": 1},
		}},
	}
}
