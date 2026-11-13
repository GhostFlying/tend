package protocol

import (
	"strings"
	"unicode/utf8"
)

// ValidPublicationPurpose describes an optional presentation marker, never a
// business completion, authorization, acceptance or installation transition.
func ValidPublicationPurpose(value string) bool {
	return value == "" || value == "progress" || value == "final"
}

// ValidateInput is the ordinary publication contract used by all transports.
// Byte limits match existing conversation output; no result state is consulted.
func (p MessagingPublication) ValidateInput() error {
	if !utf8.ValidString(p.Body) || len(p.Body) > 64<<10 || (strings.TrimSpace(p.Body) == "" && len(p.Attachments) == 0) || len(p.Attachments) > 20 || len(p.SourceMessageIDs) > 100 || len(p.IssueIDs) > 50 {
		return Errorf(CodeInvalidRequest, "nonempty bounded message or attachments required")
	}
	if !ValidPublicationPurpose(p.Purpose) {
		return Errorf(CodeInvalidRequest, "purpose must be omitted, progress or final")
	}
	if p.ReplyTo == "" && len(p.Reason) > 2000 {
		return Errorf(CodeInvalidRequest, "reason exceeds 2000 bytes")
	}
	return nil
}

func publicationSchemaConstraints() map[string]any {
	// Constraint fragments must retain their field types for schema consumers,
	// including generators that infer an untyped property as an object.
	return map[string]any{"allOf": []any{
		map[string]any{"properties": map[string]any{"body": map[string]any{"type": "string", "maxLength": 64 << 10}, "attachments": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20}, "purpose": map[string]any{"type": "string", "enum": []string{"", "progress", "final"}, "description": "Optional presentation marker only. Omit for an ordinary message; final does not finish or accept work."}}},
		map[string]any{"anyOf": []any{map[string]any{"required": []string{"body"}, "properties": map[string]any{"body": map[string]any{"type": "string", "minLength": 1, "pattern": `\S`}}}, map[string]any{"required": []string{"attachments"}, "properties": map[string]any{"attachments": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1}}}}},
	}}
}
func (MessagingPublication) JSONSchemaConstraints() map[string]any {
	return publicationSchemaConstraints()
}
func (CoordinationReply) JSONSchemaConstraints() map[string]any {
	return publicationSchemaConstraints()
}
