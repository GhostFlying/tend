package runnerapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRunnerHTTPRejectsRetiredProtocolZeroAndUnversionedHello(t *testing.T) {
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{"workspace_protocol":1}`),
		json.RawMessage(`{"workspace_protocol":1,"runtime":{"instance_id":"retired","generation":1,"sequence":1,"protocol":0,"protocol_min":0,"protocol_max":0,"mode":"normal","build":{},"capabilities":[]}}`),
	} {
		e := newEnv(t, 10*time.Millisecond)
		env := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: protocol.OpRunnerHello, Target: protocol.Target{Type: "host", ID: "linux-1"}, Payload: payload}
		status, result := e.call(asHost(), env)
		if status != http.StatusBadRequest || result.Error == nil || result.Error.Code != protocol.CodeInvalidRequest {
			t.Fatal("retired communication protocol was accepted", status, result)
		}
	}
}
