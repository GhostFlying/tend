package traex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestHostedThreadPreservesConfiguredNativeToolsOnStartAndResume(t *testing.T) {
	for _, policy := range []struct {
		name, sandbox, approval string
		config                  map[string]any
	}{
		{name: "runtime_defaults"},
		{name: "configured_execution", sandbox: "workspace-write", approval: "never", config: map[string]any{"features.shell_tool": true, "features.multi_agent": true, "mcp_servers": map[string]any{"selected": map[string]any{"enabled": true}}}},
		{name: "explicit_read_only", sandbox: "read-only", approval: "never", config: map[string]any{"features.shell_tool": false}},
	} {
		t.Run(policy.name, func(t *testing.T) {
			f, s := newFake(t)
			response := func(json.RawMessage) any {
				return map[string]any{"thread": map[string]any{"id": "configured"}, "approvalPolicy": "never"}
			}
			f.on(methodThreadStart, response)
			f.on(methodThreadResume, response)
			cfg := harness.ThreadConfig{Cwd: "workspace", Sandbox: policy.sandbox, ApprovalPolicy: policy.approval, Config: policy.config}
			th, err := s.StartThread(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ResumeThread(t.Context(), th.ID(), cfg); err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{methodThreadStart, methodThreadResume} {
				var got struct {
					Sandbox  string         `json:"sandbox"`
					Approval string         `json:"approvalPolicy"`
					Config   map[string]any `json:"config"`
				}
				if err := json.Unmarshal(f.params(method), &got); err != nil {
					t.Fatal(err)
				}
				want, _ := json.Marshal(policy.config)
				actual, _ := json.Marshal(got.Config)
				if got.Sandbox != policy.sandbox || got.Approval != policy.approval || string(actual) != string(want) {
					t.Fatal("responsibility overrode selected native policy", method, string(f.params(method)))
				}
			}
			if f.params("hooks/list") != nil {
				t.Fatal("ordinary hosted execution installed a role hook")
			}
		})
	}
}

func TestHostedSentTurnTimeoutRetainsUnknownNativeDispatch(t *testing.T) {
	f, s := newFake(t)
	f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "configured"}} })
	th, err := s.StartThread(t.Context(), harness.ThreadConfig{Cwd: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	f.on(methodTurnStart, func(json.RawMessage) any { <-release; return map[string]any{} })
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	_, err = th.StartTurn(ctx, harness.TurnInput{Text: "the real authorized operation"})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, harness.ErrRejected) || f.params(methodTurnStart) == nil {
		t.Fatal("sent operation became safe to repeat", err)
	}
}
