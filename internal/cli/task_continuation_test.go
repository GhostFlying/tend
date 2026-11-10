package cli

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestTaskContinueFreezesOriginalEnvelopeForRecovery(t *testing.T) {
	e := newTestEnv(t, "")
	id := "task_01M6F01JSS6Q9BFV6DJNSSBHEZ"
	request := "req_01M6F01JSS6Q9BFV6DJNSSBHEY"
	body := `{"reason":"Review only the rebase delta","expected_assignment_epoch":4}`
	if code := e.main("task", "continue", id, "--revision", "8", "--request-id", request, "--data", body, "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var env protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Operation != "tasks.continue" || env.RequestID != request || env.Target.ID != id || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 8 {
		t.Fatal(env)
	}
	var payload protocol.ContinueTaskPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.ExpectedAssignmentEpoch != 4 {
		t.Fatal(payload, err)
	}
}

func TestTaskContinueRejectsIncompleteRecoveryPayload(t *testing.T) {
	for _, args := range [][]string{
		{"task", "continue", "task_01M6F01JSS6Q9BFV6DJNSSBHEZ", "--revision", "8", "--data", `{"reason":""}`, "--dry-run"},
		{"task", "continue", "task_01M6F01JSS6Q9BFV6DJNSSBHEZ", "--data", `{"reason":"same goal"}`, "--dry-run"},
		{"task", "continue", "task_01M6F01JSS6Q9BFV6DJNSSBHEZ", "--revision", "8", "--data", `{"reason":"same goal","head_sha":"new"}`, "--dry-run"},
	} {
		e := newTestEnv(t, "")
		if code := e.main(args...); code == exitOK {
			t.Fatal("accepted incomplete continuation", args)
		}
	}
}
