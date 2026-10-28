package tools

import (
	"encoding/json"
	"strings"
)

// exportSchema resolves local references and distributes object properties into
// alternatives for code-mode renderers. Runtime validation uses the original.
func exportSchema(raw json.RawMessage) json.RawMessage {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		panic(err)
	}
	definitions, _ := root["definitions"].(map[string]any)
	var expand func(any, map[string]bool) any
	expand = func(value any, path map[string]bool) any {
		switch v := value.(type) {
		case []any:
			out := make([]any, len(v))
			for i, x := range v {
				out[i] = expand(x, path)
			}
			return out
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, "#/definitions/") {
				name := strings.TrimPrefix(ref, "#/definitions/")
				if path[name] {
					panic("recursive tool schema: " + name)
				}
				next := map[string]bool{}
				for k, x := range path {
					next[k] = x
				}
				next[name] = true
				target, ok := definitions[name]
				if !ok {
					panic("unresolved tool schema: " + name)
				}
				result := expand(target, next).(map[string]any)
				if name == "WorkItemEvidence" {
					// Only the server freezes these snapshots from the reference.
					// Keep them in validation, but omit them from model inputs.
					props := result["properties"].(map[string]any)
					delete(props, "observation")
					delete(props, "upgrade")
				}
				for k, x := range v {
					if k != "$ref" {
						result[k] = expand(x, path)
					}
				}
				return result
			}
			out := map[string]any{}
			for k, x := range v {
				if k != "definitions" {
					out[k] = expand(x, path)
				}
			}
			if alternatives, ok := out["anyOf"].([]any); ok && out["properties"] != nil {
				// A required-only disjunction changes validation, not argument
				// types. Keep the common object once: duplicating it in every
				// branch triggers native schema compaction and erases nested types.
				// The original schema still enforces the disjunction at execution.
				onlyRequired := len(alternatives) > 0
				choices := []string{}
				for _, a := range alternatives {
					branch := a.(map[string]any)
					if len(branch) != 1 || branch["required"] == nil {
						onlyRequired = false
						break
					}
					names := []string{}
					for _, key := range branch["required"].([]any) {
						names = append(names, key.(string))
					}
					choices = append(choices, strings.Join(names, ", "))
				}
				if onlyRequired {
					delete(out, "anyOf")
					description, _ := out["description"].(string)
					out["description"] = description + " Provide at least one required field group: " + strings.Join(choices, " OR ") + "."
					return out
				}
				for _, a := range alternatives {
					branch := a.(map[string]any)
					if branch["type"] == nil {
						branch["type"] = "object"
					}
					if branch["type"] != "object" {
						continue
					}
					props := map[string]any{}
					for k, x := range out["properties"].(map[string]any) {
						props[k] = x
					}
					if own, ok := branch["properties"].(map[string]any); ok {
						for k, x := range own {
							props[k] = x
						}
					}
					required := []any{}
					if rootRequired, ok := out["required"].([]any); ok {
						required = append(required, rootRequired...)
					}
					if own, ok := branch["required"].([]any); ok {
						for _, v := range own {
							found := false
							for _, x := range required {
								found = found || x == v
							}
							if !found {
								required = append(required, v)
							}
						}
					}
					if len(required) > 0 {
						branch["required"] = required
					}
					branch["properties"] = props
				}
			}
			return out
		default:
			return value
		}
	}
	b, err := json.Marshal(expand(root, map[string]bool{}))
	if err != nil {
		panic(err)
	}
	return b
}
