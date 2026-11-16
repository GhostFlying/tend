package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestExternalWaitRejectsDefiniteInputWithoutInventingUncertainty(t *testing.T) {
	for _, code := range []protocol.Code{protocol.CodeInvalidRequest, protocol.CodeNotFound, protocol.CodeForbidden, protocol.CodeConflictPayload, protocol.CodeStaleRevision, protocol.CodeInvalidTransition, protocol.CodeInternal} {
		t.Run(string(code), func(t *testing.T) {
			r := Results{Spec: WorkerSpec{WaitExternal: func(context.Context, protocol.WaitExternalPayload) (protocol.ExternalWait, error) {
				return protocol.ExternalWait{}, protocol.Errorf(code, "checks.target_id requires a delivery ID")
			}}}
			result := r.waitExternal(t.Context(), ToolCall{Arguments: []byte(`{"wait_key":"existing","kind":"checks","target_id":"https://invalid.example/ci","reason":"CI","recovery":"same delivery"}`)})
			definite := code != protocol.CodeInternal
			if result.Success || strings.Contains(result.Text, "correct the input") != definite || strings.Contains(result.Text, "after uncertainty") == definite {
				t.Fatal(result)
			}
		})
	}
	r := Results{Spec: WorkerSpec{WaitExternal: func(context.Context, protocol.WaitExternalPayload) (protocol.ExternalWait, error) {
		return protocol.ExternalWait{}, errors.New("network receipt lost")
	}}}
	result := r.waitExternal(t.Context(), ToolCall{Arguments: []byte(`{}`)})
	if !strings.Contains(result.Text, "preserve the original wait_key") {
		t.Fatal("uncertain outcome lost original identity", result)
	}
}
