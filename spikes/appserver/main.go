// Command appserver is a phase A spike that drives `traex app-server` over
// stdio to verify thread lifecycle, thread/inject_items, dynamic tool calls,
// compaction and resume behavior. See README.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	codeword1 = "PELICAN-42"
	codeword2 = "HERON-7"
	dupMarker = "DUP-MARKER-5531"
)

type spike struct {
	bin, model, workDir string
	tr                  *transcript
	c                   *client
	results             map[string]any

	toolMu    sync.Mutex
	toolCalls []json.RawMessage
}

func main() {
	bin := flag.String("bin", "traex", "harness binary")
	outDir := flag.String("out", "spikes/appserver/out", "output directory")
	flag.Parse()

	model := os.Getenv("TEND_TRAEX_MODEL")
	if model == "" {
		model = "GPT-5.6-Sol"
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	workDir, err := filepath.Abs(filepath.Join(*outDir, "work-"+stamp))
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		log.Fatal(err)
	}
	tr, err := newTranscript(filepath.Join(*outDir, "transcript-"+stamp+".jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	s := &spike{bin: *bin, model: model, workDir: workDir, tr: tr, results: map[string]any{}}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	if err := s.run(ctx); err != nil {
		s.results["fatal"] = err.Error()
	}
	b, _ := json.MarshalIndent(s.results, "", "  ")
	_ = os.WriteFile(filepath.Join(*outDir, "results-"+stamp+".json"), b, 0o644)
	fmt.Println(string(b))
}

func (s *spike) start(ctx context.Context) error {
	c, err := startClient(ctx, s.bin, []string{"app-server"}, s.tr, s.handleServerRequest)
	if err != nil {
		return err
	}
	s.c = c
	var initResp json.RawMessage
	err = c.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "tend-spike", "title": nil, "version": "0.0.0"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, &initResp)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return c.notify("initialized", nil)
}

func (s *spike) handleServerRequest(method string, params json.RawMessage) (any, error) {
	switch method {
	case "item/tool/call":
		s.toolMu.Lock()
		s.toolCalls = append(s.toolCalls, params)
		s.toolMu.Unlock()
		var p struct {
			CallID    string          `json:"callId"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(params, &p)
		return map[string]any{
			"success":      true,
			"contentItems": []any{map[string]any{"type": "inputText", "text": "ECHO " + p.CallID + " " + string(p.Arguments)}},
		}, nil
	default:
		return nil, fmt.Errorf("spike does not handle %s", method)
	}
}

func developerItem(text string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    "developer",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}
}

func (s *spike) inject(ctx context.Context, threadID string, texts ...string) error {
	items := make([]any, 0, len(texts))
	for _, t := range texts {
		items = append(items, developerItem(t))
	}
	return s.c.call(ctx, "thread/inject_items", map[string]any{"threadId": threadID, "items": items}, nil)
}

type turnResult struct {
	TurnID  string   `json:"turnId"`
	Status  string   `json:"status"`
	Text    string   `json:"text"`
	Items   []string `json:"itemTypes"`
	Methods []string `json:"notableNotifications,omitempty"`
}

func (s *spike) startTurn(ctx context.Context, threadID, prompt string) (string, error) {
	var resp struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err := s.c.call(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"input":    []any{map[string]any{"type": "text", "text": prompt, "text_elements": []any{}}},
	}, &resp)
	return resp.Turn.ID, err
}

// waitTurn consumes notifications until the given turn completes. onStarted
// runs once when the turn reports its first started item.
func (s *spike) waitTurn(ctx context.Context, threadID, turnID string, onStarted func()) (turnResult, error) {
	res := turnResult{TurnID: turnID}
	started := false
	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case m := <-s.c.notifications:
			var p struct {
				ThreadID string          `json:"threadId"`
				TurnID   string          `json:"turnId"`
				Item     json.RawMessage `json:"item"`
				Turn     struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(m.Params, &p)
			switch m.Method {
			case "item/started":
				if !started && p.TurnID == turnID && onStarted != nil {
					started = true
					onStarted()
				}
			case "item/completed":
				if p.TurnID != turnID {
					continue
				}
				var item struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				_ = json.Unmarshal(p.Item, &item)
				res.Items = append(res.Items, item.Type)
				if item.Type == "agentMessage" {
					res.Text += item.Text
				}
			case "thread/compacted", "error", "warning", "model/rerouted", "model/fallback":
				res.Methods = append(res.Methods, m.Method)
			case "turn/completed":
				if p.Turn.ID == turnID {
					res.Status = p.Turn.Status
					return res, nil
				}
			}
		}
	}
}

func (s *spike) ask(ctx context.Context, threadID, key, prompt string, onStarted func()) turnResult {
	turnID, err := s.startTurn(ctx, threadID, prompt)
	if err != nil {
		s.results[key] = map[string]any{"error": err.Error()}
		return turnResult{}
	}
	res, err := s.waitTurn(ctx, threadID, turnID, onStarted)
	if err != nil {
		s.results[key] = map[string]any{"error": err.Error(), "partial": res}
		return res
	}
	s.results[key] = res
	return res
}

func (s *spike) run(ctx context.Context) error {
	if err := s.start(ctx); err != nil {
		return err
	}

	// A1: thread lifecycle with a client-defined dynamic tool.
	var started struct {
		Thread struct {
			ID   string  `json:"id"`
			Path *string `json:"path"`
		} `json:"thread"`
		Model              string   `json:"model"`
		InstructionSources []string `json:"instructionSources"`
	}
	err := s.c.call(ctx, "thread/start", map[string]any{
		"model":                 s.model,
		"cwd":                   s.workDir,
		"approvalPolicy":        "never",
		"sandbox":               "read-only",
		"developerInstructions": "You are a test agent for the tend spike. Follow instructions literally and keep answers minimal.",
		"dynamicTools": []any{map[string]any{
			"type":        "function",
			"name":        "tend_echo",
			"description": "Echo a message back. Use when asked to call tend_echo.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"message": map[string]any{"type": "string"}},
				"required":             []string{"message"},
				"additionalProperties": false,
			},
		}},
	}, &started)
	if err != nil {
		return fmt.Errorf("thread/start: %w", err)
	}
	threadID := started.Thread.ID
	s.results["a1_thread"] = map[string]any{"threadId": threadID, "path": started.Thread.Path, "model": started.Model, "instructionSources": started.InstructionSources}

	// A2a: inject between turns and check visibility.
	if err := s.inject(ctx, threadID, "[tend context packet p1 epoch=1] The project codeword is "+codeword1+"."); err != nil {
		s.results["a2a_inject_error"] = err.Error()
	}
	r := s.ask(ctx, threadID, "a2a_codeword_after_inject", "What is the project codeword? Reply with only the codeword.", nil)
	s.results["a2a_visible"] = strings.Contains(r.Text, codeword1)

	// A4': dynamic tool call identity.
	s.ask(ctx, threadID, "a4_dynamic_tool_turn", "Call the tend_echo tool with message \"hello\", then reply with the tool output verbatim.", nil)
	s.toolMu.Lock()
	s.results["a4_tool_call_params"] = s.toolCalls
	s.toolMu.Unlock()

	// A2b: inject while a turn is running.
	var midErr error
	var midOnce sync.Once
	s.ask(ctx, threadID, "a2b_long_turn", "Count from 1 to 40, one number per line, then stop.", func() {
		midOnce.Do(func() {
			midErr = s.inject(ctx, threadID, "[tend context packet p2 epoch=1] The project codeword has changed to "+codeword2+".")
		})
	})
	s.results["a2b_mid_turn_inject_error"] = errString(midErr)
	r = s.ask(ctx, threadID, "a2b_codeword_after_mid_turn", "What is the project codeword now? Reply with only the codeword.", nil)
	s.results["a2b_sees_new_codeword"] = strings.Contains(r.Text, codeword2)

	// A2c: duplicate injection of the same item.
	if err := s.inject(ctx, threadID, "[tend context packet p3 epoch=1] Marker "+dupMarker+"."); err != nil {
		s.results["a2c_first_error"] = err.Error()
	}
	if err := s.inject(ctx, threadID, "[tend context packet p3 epoch=1] Marker "+dupMarker+"."); err != nil {
		s.results["a2c_second_error"] = err.Error()
	}
	s.ask(ctx, threadID, "a2c_turn_after_dup", "How many developer messages in your context contain the text "+dupMarker+"? Reply with only a number.", nil)
	if started.Thread.Path != nil {
		s.results["a2c_rollout_marker_count"] = countInFile(*started.Thread.Path, "Marker "+dupMarker)
	}

	// A3: manual compaction, then check whether injected facts survive.
	if err := s.c.call(ctx, "thread/compact/start", map[string]any{"threadId": threadID}, nil); err != nil {
		s.results["a3_compact_error"] = err.Error()
	} else {
		s.results["a3_compaction_signals"] = s.collectFor(ctx, 3*time.Minute, "thread/compacted")
	}
	r = s.ask(ctx, threadID, "a3_codeword_after_compact", "What is the project codeword now? Reply with only the codeword, or UNKNOWN.", nil)
	s.results["a3_codeword_survives"] = strings.Contains(r.Text, codeword2)

	// A6: restart the app-server and resume the thread.
	s.c.close()
	if err := s.start(ctx); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	var resumed json.RawMessage
	if err := s.c.call(ctx, "thread/resume", map[string]any{"threadId": threadID}, &resumed); err != nil {
		s.results["a6_resume_error"] = err.Error()
	} else {
		r = s.ask(ctx, threadID, "a6_codeword_after_resume", "What is the project codeword now? Reply with only the codeword, or UNKNOWN.", nil)
		s.results["a6_codeword_survives"] = strings.Contains(r.Text, codeword2)
	}
	s.c.close()
	return nil
}

// collectFor drains notifications until one with the stop method arrives or
// the timeout passes, returning the methods and compaction items it saw.
func (s *spike) collectFor(ctx context.Context, timeout time.Duration, stop string) []string {
	var seen []string
	deadline := time.After(timeout)
	for {
		select {
		case <-ctx.Done():
			return append(seen, "ctx-done")
		case <-deadline:
			return append(seen, "timeout")
		case m := <-s.c.notifications:
			entry := m.Method
			if m.Method == "item/completed" || m.Method == "item/started" {
				var p struct {
					Item struct {
						Type string `json:"type"`
					} `json:"item"`
				}
				_ = json.Unmarshal(m.Params, &p)
				entry += ":" + p.Item.Type
			}
			seen = append(seen, entry)
			if m.Method == stop {
				return seen
			}
		}
	}
}

func countInFile(path, needle string) any {
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return strings.Count(string(b), needle)
}

func errString(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}
