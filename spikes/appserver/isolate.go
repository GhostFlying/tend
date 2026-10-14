package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// isolationOverrides turns off everything the main agent must not use: shell
// and patch tools, browsing, plugins, apps, skills and memories. Sub-agents
// stay available to the main agent.
// Keys are dotted config paths, the same form as `-c key=value`.
var isolationOverrides = map[string]any{
	"features.shell_tool":             false,
	"features.unified_exec":           false,
	"features.shell_snapshot":         false,
	"features.apply_patch_freeform":   false,
	"features.code_mode_host":         false,
	"features.browser_use":            false,
	"features.browser_use_external":   false,
	"features.in_app_browser":         false,
	"features.computer_use":           false,
	"features.image_generation":       false,
	"features.plugins":                false,
	"features.apps":                   false,
	"features.memories":               false,
	"features.goals":                  false,
	"features.skill_search":           false,
	"features.tool_suggest":           false,
	"features.workspace_dependencies": false,
	"features.workspace_undo":         false,
	"skills.bundled.enabled":          false,
	"skills.include_instructions":     false,
	"web_search":                      "disabled",
	// Gates found in the upstream tool spec plan; see docs/findings/spike-a.md.
	"tools.update_plan.enabled":                     false,
	"tools.experimental_request_user_input.enabled": false,
	"features.view_image":                           false,
	"features.code_mode":                            false,
	"features.code_mode_only":                       false,
}

// runIsolation starts a main-agent style thread in a directory outside any
// repository, with isolation overrides, and probes what remains available.
func (s *spike) runIsolation(ctx context.Context, cwd string) error {
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return err
	}
	if err := s.start(ctx); err != nil {
		return err
	}
	defer s.c.close()

	var started struct {
		Thread struct {
			ID   string  `json:"id"`
			Path *string `json:"path"`
		} `json:"thread"`
		InstructionSources []string `json:"instructionSources"`
	}
	err := s.c.call(ctx, "thread/start", map[string]any{
		"model":                 s.model,
		"cwd":                   cwd,
		"approvalPolicy":        "never",
		"sandbox":               "read-only",
		"config":                isolationOverrides,
		"developerInstructions": "You are the tend main agent in an isolation test. Use only the tools you actually have. Never pretend a tool call succeeded.",
		"dynamicTools":          []any{echoToolSpec()},
	}, &started)
	if err != nil {
		return fmt.Errorf("thread/start: %w", err)
	}
	threadID := started.Thread.ID
	s.results["iso_thread"] = map[string]any{"threadId": threadID, "instructionSources": started.InstructionSources}

	s.ask(ctx, threadID, "iso_tool_inventory",
		"List the exact names of every tool you can call in this conversation, comma separated, nothing else.", nil)

	marker := filepath.Join(cwd, "iso-write-probe.txt")
	s.ask(ctx, threadID, "iso_shell_attempt",
		"Try to create the file "+marker+" containing the word probe, using any tool you have. Then report in one line whether it worked and which tool you used.", nil)
	_, statErr := os.Stat(marker)
	s.results["iso_file_created"] = statErr == nil

	s.ask(ctx, threadID, "iso_dynamic_tool", "Call tend_echo with message \"iso\" and reply with its output verbatim.", nil)
	s.toolMu.Lock()
	s.results["iso_tool_calls"] = len(s.toolCalls)
	s.toolMu.Unlock()

	if started.Thread.Path != nil {
		s.results["iso_base_context"] = summarizeBaseContext(*started.Thread.Path)
	}
	return nil
}

func echoToolSpec() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "tend_echo",
		"description": "Echo a message back. Use when asked to call tend_echo.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"message": map[string]any{"type": "string"}},
			"required":             []string{"message"},
			"additionalProperties": false,
		},
	}
}

// summarizeBaseContext lists the items appended before the first user turn,
// with their role and size, from the rollout history mutations.
func summarizeBaseContext(path string) any {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				TurnID string `json:"turn_id"`
				Items  []struct {
					Type    string `json:"type"`
					Role    string `json:"role"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"items"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Type != "history_mutation" {
			continue
		}
		for _, it := range rec.Payload.Items {
			if it.Role == "user" && len(it.Content) > 0 && !strings.HasPrefix(it.Content[0].Text, "<") {
				return out
			}
			for _, c := range it.Content {
				head := c.Text
				if i := strings.IndexAny(head, "\n"); i >= 0 {
					head = head[:i]
				}
				if len(head) > 60 {
					head = head[:60]
				}
				out = append(out, fmt.Sprintf("%s/%s chars=%d %q", it.Type, it.Role, len(c.Text), head))
			}
		}
	}
	return out
}
