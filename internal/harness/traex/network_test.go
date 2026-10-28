package traex

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestNetworkPolicySurvivesResumeWithoutWideningFiles(t *testing.T) {
	for _, sandbox := range []string{"read-only", "workspace-write"} {
		t.Run(sandbox, func(t *testing.T) {
			f, s := newFake(t)
			native := map[string]any{"type": "readOnly", "networkAccess": false}
			if sandbox == "workspace-write" {
				native = map[string]any{"type": "workspaceWrite", "networkAccess": false, "writableRoots": []any{"/workspace"}, "excludeSlashTmp": true, "excludeTmpdirEnvVar": true}
			}
			reply := func(json.RawMessage) any {
				return map[string]any{"thread": map[string]any{"id": "network"}, "sandbox": native}
			}
			f.on(methodThreadStart, reply)
			f.on(methodThreadResume, reply)
			f.on(methodTurnStart, func(raw json.RawMessage) any {
				var p TurnStartParams
				_ = json.Unmarshal(raw, &p)
				want := make(map[string]any, len(native))
				for key, value := range native {
					want[key] = value
				}
				want["networkAccess"] = true
				if !reflect.DeepEqual(p.SandboxPolicy, want) {
					t.Errorf("permission override = %#v, want %#v", p.SandboxPolicy, want)
				}
				return map[string]any{"turn": map[string]any{"id": "turn"}}
			})
			cfg := harness.ThreadConfig{Sandbox: sandbox, NetworkAccess: true}
			th, err := s.StartThread(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := th.StartTurn(context.Background(), harness.TurnInput{Text: "check"}); err != nil {
				t.Fatal(err)
			}
			th, err = s.ResumeThread(t.Context(), th.ID(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := th.StartTurn(context.Background(), harness.TurnInput{Text: "check again"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
