package traex

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var (
	schemaDir    = flag.String("schema-dir", "", "traex JSON Schema bundle to extract generated/schema.json from")
	traexVersion = flag.String("traex-version", "", "traex version the schema bundle came from")
)

const subsetPath = "generated/schema.json"

// checked lists every hand-written message and where traex defines it.
// Sent messages must supply every required field; received ones may read
// a subset. A selector picks a variant of a oneOf: "type=X" by the type
// tag, "has=X" by a property only that variant has.
var checked = []struct {
	name, file, def, selector string
	v                         any
	sent                      bool
}{
	{"InitializeParams", "v1/InitializeParams.json", "", "", InitializeParams{}, true},
	{"ClientInfo", "v1/InitializeParams.json", "ClientInfo", "", ClientInfo{}, true},
	{"InitializeCapabilities", "v1/InitializeParams.json", "InitializeCapabilities", "", InitializeCapabilities{}, true},
	{"ThreadStartParams", "v2/ThreadStartParams.json", "", "", ThreadStartParams{}, true},
	{"DynamicToolSpec", "v2/ThreadStartParams.json", "DynamicToolSpec", "has=inputSchema", DynamicToolSpec{}, true},
	{"ThreadResumeParams", "v2/ThreadResumeParams.json", "", "", ThreadResumeParams{}, true},
	{"ThreadStartResponse", "v2/ThreadStartResponse.json", "", "", ThreadStartResponse{}, false},
	{"Thread", "v2/ThreadStartResponse.json", "Thread", "", Thread{}, false},
	{"ThreadInjectItemsParams", "v2/ThreadInjectItemsParams.json", "", "", ThreadInjectItemsParams{}, true},
	{"TurnStartParams", "v2/TurnStartParams.json", "", "", TurnStartParams{}, true},
	{"UserInput", "v2/TurnStartParams.json", "UserInput", "type=text", UserInput{}, true},
	{"TurnStartResponse", "v2/TurnStartResponse.json", "", "", TurnStartResponse{}, false},
	{"Turn", "v2/TurnStartResponse.json", "Turn", "", Turn{}, false},
	{"TurnInterruptParams", "v2/TurnInterruptParams.json", "", "", TurnInterruptParams{}, true},
	{"DynamicToolCallParams", "DynamicToolCallParams.json", "", "", DynamicToolCallParams{}, false},
	{"DynamicToolCallResponse", "DynamicToolCallResponse.json", "", "", DynamicToolCallResponse{}, true},
	{"ToolResponseItem", "DynamicToolCallResponse.json", "DynamicToolCallResponseContentItem", "type=inputText", ToolResponseItem{}, true},
	{"TurnNotification", "v2/TurnCompletedNotification.json", "", "", TurnNotification{}, false},
	{"ItemNotification", "v2/ItemCompletedNotification.json", "", "", ItemNotification{}, false},
	{"ThreadItem", "v2/ItemCompletedNotification.json", "ThreadItem", "type=agentMessage", ThreadItem{}, false},
	{"DynamicToolCallItem", "v2/ItemCompletedNotification.json", "ThreadItem", "type=dynamicToolCall", DynamicToolCallItem{}, false},
	{"McpToolCallItem", "v2/ItemCompletedNotification.json", "ThreadItem", "type=mcpToolCall", McpToolCallItem{}, false},
	{"McpToolResult", "v2/ItemCompletedNotification.json", "McpToolCallResult", "", McpToolResult{}, false},
	{"McpToolError", "v2/ItemCompletedNotification.json", "McpToolCallError", "", McpToolError{}, false},
	{"CommandItem", "v2/ItemCompletedNotification.json", "ThreadItem", "type=commandExecution", CommandItem{}, false},
	{"WebSearchItem", "v2/ItemCompletedNotification.json", "ThreadItem", "type=webSearch", WebSearchItem{}, false},
	{"ImageViewItem", "v2/ItemCompletedNotification.json", "ThreadItem", "type=imageView", ImageViewItem{}, false},
	{"CollabAgentItem", "v2/ItemCompletedNotification.json", "ThreadItem", "type=collabAgentToolCall", CollabAgentItem{}, false},
}

type subset struct {
	TraexVersion string           `json:"traex_version"`
	Generator    string           `json:"generator"`
	Messages     map[string]shape `json:"messages"`
}

type shape struct {
	Properties []string `json:"properties"`
	Required   []string `json:"required"`
}

func jsonFields(v any) []string {
	var out []string
	t := reflect.TypeOf(v)
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

// TestSchemaSubset checks the hand-written messages against the extracted
// schema. With -schema-dir it first regenerates generated/schema.json.
func TestSchemaSubset(t *testing.T) {
	if *schemaDir != "" {
		if err := extract(*schemaDir, *traexVersion); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(subsetPath)
	if err != nil {
		t.Fatal(err)
	}
	var s subset
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	for _, c := range checked {
		want, ok := s.Messages[c.name]
		if !ok {
			t.Errorf("%s is missing from %s; regenerate it", c.name, subsetPath)
			continue
		}
		fields := jsonFields(c.v)
		for _, f := range fields {
			if !slices.Contains(want.Properties, f) {
				t.Errorf("%s.%s is not in traex %s's schema", c.name, f, s.TraexVersion)
			}
		}
		if !c.sent {
			continue
		}
		for _, r := range want.Required {
			if !slices.Contains(fields, r) {
				t.Errorf("%s lacks %s, which traex %s requires", c.name, r, s.TraexVersion)
			}
		}
	}
}

func extract(dir, version string) error {
	if version == "" {
		return fmt.Errorf("-traex-version is required with -schema-dir")
	}
	out := subset{
		TraexVersion: version,
		Generator:    "traex app-server generate-json-schema --experimental",
		Messages:     map[string]shape{},
	}
	for _, c := range checked {
		b, err := os.ReadFile(filepath.Join(dir, c.file))
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", c.file, err)
		}
		node := doc
		if c.def != "" {
			defs, _ := doc["definitions"].(map[string]any)
			if node, _ = defs[c.def].(map[string]any); node == nil {
				return fmt.Errorf("%s has no definition %s", c.file, c.def)
			}
		}
		if c.selector != "" {
			if node = pick(node, c.selector); node == nil {
				return fmt.Errorf("%s/%s has no variant %s", c.file, c.def, c.selector)
			}
		}
		var sh shape
		props, _ := node["properties"].(map[string]any)
		for k := range props {
			sh.Properties = append(sh.Properties, k)
		}
		for _, r := range asSlice(node["required"]) {
			sh.Required = append(sh.Required, r.(string))
		}
		slices.Sort(sh.Properties)
		slices.Sort(sh.Required)
		out.Messages[c.name] = sh
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(subsetPath, append(b, '\n'), 0o644)
}

// pick selects a variant of a oneOf or anyOf.
func pick(node map[string]any, selector string) map[string]any {
	kind, value, _ := strings.Cut(selector, "=")
	variants := append(asSlice(node["oneOf"]), asSlice(node["anyOf"])...)
	for _, v := range variants {
		m, _ := v.(map[string]any)
		props, _ := m["properties"].(map[string]any)
		switch kind {
		case "has":
			if _, ok := props[value]; ok {
				return m
			}
		case "type":
			typ, _ := props["type"].(map[string]any)
			if slices.Contains(asSlice(typ["enum"]), any(value)) || typ["const"] == value {
				return m
			}
		}
	}
	return nil
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
