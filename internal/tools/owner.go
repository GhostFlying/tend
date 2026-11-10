package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/protocol/schema"
)

// OwnerTools is injected by assembly; tools does not import the API layer.
type OwnerTools interface {
	Specs() []protocol.OwnerToolSpec
	Call(context.Context, protocol.OwnerToolCall) (any, error)
}

func (s *Service) SetOwnerTools(owner OwnerTools) { s.owner = owner }

func (s *Service) HasOwnerTools() bool { return s != nil && s.owner != nil }

type ownerCatalogArgs struct {
	Filter     string `json:"filter,omitempty"`
	After      string `json:"after,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Name       string `json:"name,omitempty"`
	SchemaType string `json:"schema_type,omitempty"`
}

type ownerInvokeArgs struct {
	Name      string                      `json:"name"`
	Arguments protocol.OwnerToolArguments `json:"arguments"`
}

// NamedSpecs keeps the complete owner catalog discoverable without putting
// every API schema into every main turn's context.
func (s *Service) NamedSpecs(purpose agentconfig.Purpose) []harness.ToolSpec {
	out := NamedSpecs(purpose)
	if purpose == agentconfig.Main && s.HasOwnerTools() {
		specs := s.owner.Specs()
		for i := range out {
			for _, m := range hostedMutations {
				if out[i].Name != m.name || communicationMutation(m.name) {
					continue
				}
				for _, spec := range specs {
					if spec.Capability == m.command.Operation() {
						out[i].InputSchema = hostedMutationSchema(m.payload)
						out[i].Description = "Use the owner's " + spec.Capability + " capability with the same domain validation. Source references preserve provenance when supplied; they are not an extra main-Agent permission grant."
					}
				}
			}
		}
		out = append(out, harness.ToolSpec{Name: "owner_capabilities", Description: "Discover the complete actual owner business, administration and diagnostic catalog. Filter/after/limit page names; name returns one exact invocation schema. schema_type returns a canonical owner API type schema; use filter to discover type names. Read this before invoking an unfamiliar capability. No write or external probe.", InputSchema: exportSchema(json.RawMessage(argumentSchema(ownerCatalogArgs{})))})
		out = append(out, harness.ToolSpec{Name: "owner_call", Description: "Invoke one concrete registered owner capability discovered with owner_capabilities. Use its exact name and arguments. Same authority and domain rules as owner UI/CLI, actual actor is main Agent. Positive effect_seq identifies each new mutation; preserve original_envelope only for exact receipt replay. path/query preserve all pagination and selectors. Unknown outcomes require original request reconciliation.", InputSchema: exportSchema(json.RawMessage(argumentSchema(ownerInvokeArgs{})))})
	}
	return out
}

func (s *Service) ownerCall(ctx context.Context, c Call) (harness.ToolResult, bool) {
	if s.owner == nil {
		return harness.ToolResult{}, false
	}
	name := c.Tool
	arguments := c.Arguments
	if name == "owner_capabilities" {
		if _, err := s.app.MainOwner(ctx, c.Principal, c.executionRequest()); err != nil {
			return s.answerHosted(ctx, c, nil, err), true
		}
		var args ownerCatalogArgs
		if err := json.Unmarshal(arguments, &args); err != nil {
			return s.answerHosted(ctx, c, nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid catalog arguments")), true
		}
		value, err := s.ownerCatalog(args)
		return s.answerHosted(ctx, c, value, err), true
	}
	if name == "owner_call" {
		var args ownerInvokeArgs
		if err := json.Unmarshal(arguments, &args); err != nil {
			return s.answerHosted(ctx, c, nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid owner invocation")), true
		}
		name = args.Name
		arguments, _ = json.Marshal(args.Arguments)
		if !strings.HasPrefix(name, "owner_") || name == "owner_call" || name == "owner_capabilities" {
			return s.answerHosted(ctx, c, nil, protocol.Errorf(protocol.CodeInvalidRequest, "select a concrete registered capability")), true
		}
	}
	if !strings.HasPrefix(name, "owner_") {
		var mutation *hostedMutation
		for _, m := range hostedMutations {
			if m.name == name {
				copy := m
				mutation = &copy
				break
			}
		}
		if mutation == nil {
			return harness.ToolResult{}, false
		}
		capability := mutation.command.Operation()
		name = ""
		for _, spec := range s.owner.Specs() {
			if spec.Capability == capability {
				name = spec.Name
				break
			}
		}
		if name == "" {
			return harness.ToolResult{}, false
		}
		var legacy hostedMutationArgs
		if err := json.Unmarshal(arguments, &legacy); err != nil {
			return s.answerHosted(ctx, c, nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid owner arguments")), true
		}
		arguments, _ = json.Marshal(protocol.OwnerToolArguments{EffectSeq: legacy.EffectSeq, Target: protocol.Target{Type: mutation.target, ID: legacy.TargetID}, ExpectedControlRevision: legacy.ExpectedControlRevision, Payload: legacy.Payload})
	}
	request := c.RequestID
	if request == "" {
		if c.Native == "" {
			return s.answerHosted(ctx, c, nil, protocol.Errorf(protocol.CodeMissingRequestID, "tool callback has no native identity")), true
		}
		var err error
		request, err = s.app.NativeRequestID(ctx, "harness:"+c.Principal.Subject, c.Native)
		if err != nil {
			return s.answerHosted(ctx, c, nil, err), true
		}
	}
	result, err := s.owner.Call(ctx, protocol.OwnerToolCall{Actor: c.Principal, ExecutionID: c.executionRequest(), RequestID: request, Name: name, Arguments: arguments})
	return s.answerHosted(ctx, c, result, err), true
}

func (s *Service) ownerCatalog(args ownerCatalogArgs) (any, error) {
	if args.SchemaType != "" || args.Filter != "" && strings.HasPrefix(args.Filter, "type:") {
		gen := schema.New()
		for _, enum := range protocol.APIEnums() {
			gen.Enum(enum)
		}
		for _, value := range protocol.APITypes() {
			gen.Add(value)
		}
		data, err := gen.Document("owner API")
		if err != nil {
			return nil, err
		}
		var root map[string]any
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, err
		}
		defs := root["definitions"].(map[string]any)
		if args.SchemaType != "" {
			body, ok := defs[args.SchemaType].(map[string]any)
			if !ok {
				return nil, protocol.Errorf(protocol.CodeNotFound, "owner API type does not exist")
			}
			out := map[string]any{}
			for key, value := range body {
				out[key] = value
			}
			// Results retain normal JSON Schema references. Include only reachable
			// definitions; output types may be recursive and are not tool inputs.
			reachable := map[string]any{}
			var visit func(any)
			visit = func(value any) {
				switch value := value.(type) {
				case map[string]any:
					if ref, ok := value["$ref"].(string); ok && strings.HasPrefix(ref, "#/definitions/") {
						name := strings.TrimPrefix(ref, "#/definitions/")
						if _, found := reachable[name]; !found {
							reachable[name] = defs[name]
							visit(defs[name])
						}
					}
					for _, child := range value {
						visit(child)
					}
				case []any:
					for _, child := range value {
						visit(child)
					}
				}
			}
			visit(body)
			out["definitions"] = reachable
			schemaJSON, _ := json.Marshal(out)
			return map[string]any{"schema_type": args.SchemaType, "schema": json.RawMessage(schemaJSON)}, nil
		}
		var names []string
		for name := range defs {
			if strings.Contains(strings.ToLower(name), strings.ToLower(strings.TrimPrefix(args.Filter, "type:"))) && name > args.After {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		limit := args.Limit
		if limit <= 0 || limit > 25 {
			limit = 25
		}
		more := len(names) > limit
		if more {
			names = names[:limit]
		}
		next := ""
		if len(names) > 0 {
			next = names[len(names)-1]
		}
		return map[string]any{"types": names, "next": next, "more": more}, nil
	}
	limit := args.Limit
	if limit <= 0 || limit > 25 {
		limit = 25
	}
	var items []protocol.OwnerToolSpec
	more := false
	for _, spec := range s.owner.Specs() {
		if args.Name != "" {
			if spec.Name == args.Name || spec.Capability == args.Name {
				return spec, nil
			}
			continue
		}
		if spec.Capability <= args.After || !strings.Contains(strings.ToLower(spec.Capability), strings.ToLower(args.Filter)) {
			continue
		}
		if len(items) == limit {
			more = true
			break
		}
		spec.InputSchema = nil
		items = append(items, spec)
	}
	if args.Name != "" {
		return nil, protocol.Errorf(protocol.CodeNotFound, "owner capability does not exist")
	}
	next := ""
	if len(items) > 0 {
		next = items[len(items)-1].Capability
	}
	return map[string]any{"items": items, "next": next, "more": more}, nil
}
