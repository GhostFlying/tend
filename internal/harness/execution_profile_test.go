package harness

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAcceptanceProfileDoesNotChangeImplementation(t *testing.T) {
	role := &protocol.ExecutionProfile{Model: "review-model", Effort: "xhigh"}
	task := &protocol.ExecutionProfile{Model: "task-review-model"}
	for _, tc := range []struct {
		spec WorkerSpec
		want protocol.ExecutionProfile
	}{
		{WorkerSpec{Role: protocol.RoleImplement, Execution: task}, protocol.ExecutionProfile{Model: "implementation", Effort: "high"}},
		{WorkerSpec{Role: protocol.RoleAccept}, *role},
		{WorkerSpec{Role: protocol.RoleAccept, Execution: task}, *task},
	} {
		if got := WorkerProfile(tc.spec, "implementation", "high", role); got != tc.want {
			t.Fatalf("selection %+v, want %+v", got, tc.want)
		}
	}
}
