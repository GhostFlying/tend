package orca

import (
	"context"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNamedExecutionUsesTerminalAndExactRoleProfile(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		t.Run(string(role), func(t *testing.T) {
			f := newWorkerFixture(t)
			f.ws.opts.TerminalSurface = true
			spec := f.spec(role)
			spec.FrozenExecution = &protocol.ExecutionProfile{Model: "named", Effort: "xhigh"}
			spec.RoleInstructions = "PRIVATE_NAMED_INSTRUCTIONS"
			ctx, cancel := context.WithCancelCause(t.Context())
			w, err := f.ws.Start(ctx, spec)
			if err != nil {
				cancel(err)
				t.Fatal(err)
			}
			f.orca.mu.Lock()
			found := false
			for _, args := range f.orca.calls {
				if len(args) > 1 && args[0] == "agent" && args[1] == "launch" {
					found = true
					if flag(args, "--surface") != "terminal" || flag(args, "--model") != "named" || flag(args, "--effort") != "xhigh" || !strings.Contains(flag(args, "--prompt"), "PRIVATE_NAMED_INSTRUCTIONS") {
						t.Error("native CLI ignored named request", args)
					}
				}
			}
			f.orca.mu.Unlock()
			cancel(context.Canceled)
			for range w.Events() {
			}
			_, _ = w.Wait()
			if !found {
				t.Fatal("no native launch")
			}
		})
	}
}
