package traex

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestReceiptHistoryDoesNotChangeReusableNativePolicy(t *testing.T) {
	f, s := newFake(t)
	policy := "danger-full-access"
	resumes := 0
	f.on(methodThreadResume, func(raw json.RawMessage) any {
		resumes++
		var p ThreadResumeParams
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Error(err)
		}
		if p.Sandbox != "" {
			policy = p.Sandbox
		}
		return ThreadStartResponse{Thread: Thread{ID: "original"}, Model: "selected", ApprovalPolicy: "never", Sandbox: map[string]any{"type": policy}}
	})
	f.on("thread/read", func(raw json.RawMessage) any {
		var p struct {
			ID    string `json:"threadId"`
			Turns bool   `json:"includeTurns"`
		}
		if err := json.Unmarshal(raw, &p); err != nil || p.ID != "original" {
			t.Errorf("history input = %s, %v", raw, err)
		}
		return map[string]any{"thread": map[string]any{"id": "original", "path": "original.jsonl", "turns": []any{map[string]any{"id": "original-turn", "status": "completed"}}}}
	})
	// Exercise the same thread before and after the transient receipt attachment.
	if _, err := s.ResumeThread(t.Context(), "original", harness.ThreadConfig{Model: "selected"}); err != nil {
		t.Fatal(err)
	}
	history, err := s.ResumeThread(t.Context(), "original", harness.ThreadConfig{Purpose: harness.ReceiptHistoryPurpose, Sandbox: "read-only", ApprovalPolicy: "never", Config: map[string]any{"features.shell_tool": false}})
	if err != nil {
		t.Fatal(err)
	}
	if resumes != 1 || policy != "danger-full-access" || history.(harness.ThreadLocation).RolloutPath() != "original.jsonl" {
		t.Fatal("history attachment mutated the original native thread", resumes, policy)
	}
	if _, ok := history.(harness.PermissionedThread); ok {
		t.Fatal("history read invented native permission readback")
	}
	if _, ok := history.(harness.ConfiguredThread); ok {
		t.Fatal("history read invented native model readback")
	}
	if _, err := history.StartTurn(t.Context(), harness.TurnInput{}); !errors.Is(err, harness.ErrRejected) {
		t.Fatal("history admitted model execution", err)
	}
	if err := history.InjectDeveloper(t.Context(), "change"); !errors.Is(err, harness.ErrRejected) {
		t.Fatal("history admitted instruction mutation", err)
	}
	if err := history.Interrupt(t.Context(), "original-turn"); !errors.Is(err, harness.ErrRejected) {
		t.Fatal("history admitted interruption", err)
	}
	receipt, found, err := history.(harness.TurnReader).FindTurn(t.Context(), "original-input", "original-turn")
	if err != nil || !found || receipt.ID != "original-turn" || receipt.Status != "completed" {
		t.Fatal("original native receipt was lost", receipt, found, err)
	}
	ordinary, err := s.ResumeThread(t.Context(), "original", harness.ThreadConfig{Model: "selected"})
	if err != nil || resumes != 2 || ordinary.(harness.PermissionedThread).PermissionDefaults().Sandbox != "danger-full-access" {
		t.Fatal("ordinary continuation inherited the receipt constraint", resumes, policy, err)
	}
	if f.params(methodTurnStart) != nil || f.params(methodThreadStart) != nil {
		t.Fatal("history recovery created native business work")
	}
}

func TestReceiptHistoryRequiresActualOriginal(t *testing.T) {
	for _, mode := range []string{"empty", "different", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newFake(t)
			if mode != "unavailable" {
				f.on("thread/read", func(json.RawMessage) any {
					return map[string]any{"thread": map[string]any{"id": "another"}}
				})
			}
			id := "original"
			if mode == "empty" {
				id = ""
			}
			if _, err := s.ReadThreadHistory(t.Context(), id); err == nil {
				t.Fatal("unavailable or different history was accepted")
			}
			if f.params(methodThreadResume) != nil || f.params(methodThreadStart) != nil {
				t.Fatal("unavailable history fell back to execution")
			}
		})
	}
}
