//go:build integration

package traex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

// A real native event loop against a controlled Responses endpoint establishes
// precisely when pending steer input becomes a user item and a model request.
func TestIntegrationNativeSteerBoundary(t *testing.T) {
	for _, phase := range []string{"streaming", "near-final", "recovery"} {
		t.Run(phase, func(t *testing.T) { nativeSteerBoundary(t, phase) })
	}
}

func nativeSteerBoundary(t *testing.T, phase string) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	first, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		raw, _ := json.Marshal(body["input"])
		if n == 2 && phase != "recovery" && (!strings.Contains(string(raw), "STEER_MARKER") || !strings.Contains(string(raw), "SECOND_MARKER")) {
			t.Error("model request lost ordered steer inputs")
		}
		if n == 2 && phase == "recovery" && (strings.Contains(string(raw), "STEER_MARKER") || strings.Contains(string(raw), "SECOND_MARKER")) {
			t.Error("unconsumed pending steer leaked into a later turn after process recovery")
		}
		t.Logf("model request %d includes first=%v second=%v", n, strings.Contains(string(raw), "STEER_MARKER"), strings.Contains(string(raw), "SECOND_MARKER"))
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b); w.(http.Flusher).Flush() }
		send(map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprint(n)}})
		if n == 1 {
			if phase == "near-final" {
				send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "reply1", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "ORIGINAL"}}}})
			} else {
				send(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "reply1", "role": "assistant", "content": []any{}}})
				send(map[string]any{"type": "response.output_text.delta", "item_id": "reply1", "output_index": 0, "content_index": 0, "delta": "ORIGINAL"})
			}
			close(first)
			select {
			case <-release:
			case <-ctx.Done():
				return
			case <-r.Context().Done():
				return
			}
		}
		text := "ORIGINAL"
		if n > 1 {
			text = "ADJUSTED"
		}
		if n != 1 || phase != "near-final" {
			send(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": fmt.Sprint("reply", n), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}})
		}
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprint(n), "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
	}))
	defer backend.Close()
	home, cwd := t.TempDir(), t.TempDir()
	config := fmt.Sprintf("model_provider = \"steer_probe\"\n[model_providers.steer_probe]\nname = \"steer probe\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\n", backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "traecli.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Start(ctx, Options{Dir: cwd, Env: []string{"TRAE_HOME=" + home, "TEND_ADDON=off"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	th, err := s.StartThread(ctx, harness.ThreadConfig{Cwd: cwd, Model: "probe", Sandbox: "read-only", ApprovalPolicy: "never"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := th.StartTurn(ctx, harness.TurnInput{Text: "ORIGINAL_GOAL"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := th.(harness.TurnSteerer).SteerTurn(ctx, id, harness.TurnInput{Text: "STEER_MARKER"}); err != nil {
		t.Fatal(err)
	}
	if err := th.(harness.TurnSteerer).SteerTurn(ctx, id, harness.TurnInput{Text: "SECOND_MARKER"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("accepted thread=%s turn=%s before original model stream released", th.ID(), id)
	if phase == "recovery" {
		if err := s.Close(); err != nil {
			t.Logf("closed isolated original app-server: %v", err)
		}
		s2, err := Start(ctx, Options{Dir: cwd, Env: []string{"TRAE_HOME=" + home, "TEND_ADDON=off"}})
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()
		resumed, err := s2.ResumeThread(ctx, th.ID(), harness.ThreadConfig{Cwd: cwd, Model: "probe", Sandbox: "read-only", ApprovalPolicy: "never"})
		if err != nil {
			t.Fatal(err)
		}
		newTurn, err := resumed.StartTurn(ctx, harness.TurnInput{Text: "RECOVERY_CHECK"})
		if err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case ev, ok := <-resumed.Events():
				if !ok {
					t.Fatal("resumed thread ended")
				}
				if ev.ItemType == harness.ItemUserMessage && (ev.Text == "STEER_MARKER" || ev.Text == "SECOND_MARKER") {
					t.Fatal("pending input was replayed in a later turn")
				}
				if ev.Kind == harness.EventTurnCompleted && ev.TurnID == newTurn {
					if ev.TurnStatus != "completed" || calls.Load() != 2 {
						t.Fatal("recovery failed")
					}
					t.Logf("same thread resumed with new explicit turn=%s; uncertain pending inputs were not replayed", newTurn)
					return
				}
			}
		}
	}
	close(release)
	consumed := 0
	streamed := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case ev, ok := <-th.Events():
			if !ok {
				t.Fatal("thread ended")
			}
			t.Logf("native event kind=%s turn=%s type=%s text=%q raw=%s", ev.Kind, ev.TurnID, ev.ItemType, ev.Text, ev.Raw)
			// Context usage is a thread-level notification with no turn ID.
			if ev.TurnID != "" && ev.TurnID != id {
				t.Fatalf("parallel/new turn %s", ev.TurnID)
			}
			if ev.Kind == harness.EventItemCompleted && ev.ItemType == harness.ItemUserMessage && ev.Text == "STEER_MARKER" {
				if consumed != 0 {
					t.Fatal("first input out of order")
				}
				consumed++
			}
			if ev.Kind == harness.EventItemCompleted && ev.ItemType == harness.ItemUserMessage && ev.Text == "SECOND_MARKER" {
				if consumed != 1 {
					t.Fatal("second input out of order")
				}
				consumed++
			}
			if ev.Kind == harness.EventAgentMessageDelta {
				streamed = true
			}
			if ev.Kind == harness.EventTurnCompleted {
				if consumed != 2 || calls.Load() != 2 || ev.TurnStatus != "completed" || (phase == "streaming" && !streamed) {
					t.Fatalf("consumed=%v calls=%d status=%s", consumed, calls.Load(), ev.TurnStatus)
				}
				if err := th.(harness.TurnSteerer).SteerTurn(ctx, id, harness.TurnInput{Text: "TOO_LATE"}); !errors.Is(err, harness.ErrRejected) {
					t.Fatalf("idle turn steer=%v", err)
				}
				return
			}
		}
	}
}
