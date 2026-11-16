package cli

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestReliabilityRecoveryCLIUsesOriginalNamedCommandsAndControls(t *testing.T) {
	for _, tc := range []struct{ command, payload string }{
		{"review-delivery", `{"delivery_id":"pr","action":"wait","expected_review_revision":11,"reason":"Original reviewer stopped without a quality conclusion"}`},
		{"review-delivery", `{"delivery_id":"pr","action":"recheck","reviewer_task_id":"original-reviewer","expected_head_sha":"head","expected_base_sha":"base","expected_requirements_digest":"digest","reason":"Review the exact read candidate"}`},
		{"update-delivery", `{"task_id":"original-task","action":"recover_preparation","expected_task_control_revision":5,"reason":"Original legacy execution stopped before any upgrade"}`},
	} {
		t.Run(tc.command+tc.payload, func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			if code := e.main("issue", tc.command, "original-issue", "--revision", "7", "--epoch", "3", "--data", tc.payload, "--dry-run", "--json"); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
			var env protocol.Envelope
			if err := json.Unmarshal(e.out.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(env.Payload, &fields); err != nil {
				t.Fatal(err)
			}
			if env.Target.ID != "original-issue" || env.RequestID == "" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || fields["expected_assignment_epoch"] != float64(3) || len(f.requests) != 0 {
				t.Fatal("recovery dropped original controls or mutated during preview", env, fields)
			}
		})
	}
}
