package runner

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOutcomeSaysWhatTheRunReported(t *testing.T) {
	for _, c := range []struct {
		op, payload, want string
	}{
		{protocol.OpAcceptancesSubmit, `{"verdict":"passed"}`, "[verdict passed]"},
		{protocol.OpRunsRelease, `{"reason":"no_changes"}`, "[reason no_changes]"},
		{protocol.OpRunsSubmitArtifact, `{"mounts":[{"path":"a","changed":true},{"path":"b"},{"path":"c","changed":true}]}`, "[changed a,c]"},
	} {
		got := fmt.Sprint(outcome(protocol.Envelope{Operation: c.op, Payload: json.RawMessage(c.payload)}))
		if got != c.want {
			t.Errorf("outcome(%s %s) = %s, want %s", c.op, c.payload, got, c.want)
		}
	}
}
