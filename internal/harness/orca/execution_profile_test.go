package orca

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRoleSelectionReachesNativeLaunch(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			f := newWorkerFixture(t)
			f.ws.opts.Model, f.ws.opts.Effort = "implementation", "high"
			f.ws.opts.Acceptance = &protocol.ExecutionProfile{Model: "role-review", Effort: "medium"}
			spec := f.spec(role)
			spec.Execution = &protocol.ExecutionProfile{Model: "task-review", Effort: "xhigh"}
			ctx, cancel := context.WithCancelCause(t.Context())
			w, err := f.ws.Start(ctx, spec)
			if err != nil {
				cancel(err)
				t.Fatal(err)
			}
			model, effort := "implementation", "high"
			if role == protocol.RoleAccept {
				model, effort = "task-review", "xhigh"
			}
			f.orca.mu.Lock()
			found := false
			for _, args := range f.orca.calls {
				if len(args) > 1 && args[0] == "agent" && args[1] == "launch" {
					found = true
					if flag(args, "--model") != model || flag(args, "--effort") != effort {
						t.Errorf("native launch %v", args)
					}
				}
			}
			f.orca.mu.Unlock()
			cancel(context.Canceled)
			for range w.Events() {
			}
			_, _ = w.Wait()
			if !found {
				t.Fatal("missing native launch")
			}
		})
	}
}
