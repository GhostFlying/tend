package runnerapi

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// This test deliberately uses only the released opaque event shape. It is also
// executed unchanged against the fixed pre-identity handler with a Go overlay.
func TestNativeIdentityOpaqueEventsRemainCompatible(t *testing.T) {
	for _, wire := range []int64{protocol.PreviousRunnerProtocol, protocol.CurrentRunnerProtocol} {
		t.Run(string(rune('0'+wire)), func(t *testing.T) {
			e := newEnv(t, time.Millisecond)
			e.createTask()
			var claim protocol.ClaimPayload
			if wire > 0 {
				runtime := &protocol.RunnerRuntime{InstanceID: "identity-compat", Generation: 1, Sequence: 1, Protocol: wire, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal}
				e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "", protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: runtime}))
				runtime.Sequence++
				claim = protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}
			}
			as := e.claim(e.envelope(protocol.OpRunnerClaim, "", claim))
			if as == nil {
				t.Fatal("no assignment")
			}
			raw := json.RawMessage(`{"provider":"orca","provider_id":"worktree","terminal":"terminal","pane":"tab:pane","session_id":"provider-session","state":"known","controller_observation":"unknown"}`)
			payload := protocol.EventsPayload{Events: []protocol.RunEvent{{Seq: 1, NativeID: "actual-native-record", Kind: "worker.native_identity", Body: raw}}}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var oldPayload struct {
				Events []struct {
					Seq      int64           `json:"seq"`
					NativeID string          `json:"native_id,omitempty"`
					Kind     string          `json:"kind"`
					Body     json.RawMessage `json:"body,omitempty"`
				} `json:"events"`
			}
			decoder := json.NewDecoder(bytes.NewReader(encoded))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&oldPayload); err != nil {
				t.Fatal("old strict shape rejected a new wire field", err)
			}
			var original protocol.RunObserved
			if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
				run, err := store.GetRun(t.Context(), tx.Q(), as.RunID)
				original = run.ObservedState
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var accepted protocol.EventsResult
			if err := json.Unmarshal(e.mustOK(asRun(*as), e.envelope(protocol.OpRunnerEvents, as.RunID, payload)), &accepted); err != nil || accepted.Accepted != 1 {
				t.Fatal(accepted, err)
			}
			if err := json.Unmarshal(e.mustOK(asRun(*as), e.envelope(protocol.OpRunnerEvents, as.RunID, payload)), &accepted); err != nil || accepted.Accepted != 0 {
				t.Fatal("replay was not original event receipt", accepted, err)
			}
			if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var saved string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM run_logs WHERE run_id=? AND seq=1`, as.RunID).Scan(&saved); err != nil {
					return err
				}
				if saved != string(raw) {
					t.Fatal("opaque body changed", saved)
				}
				run, err := store.GetRun(t.Context(), tx.Q(), as.RunID)
				if run.ObservedState != original {
					t.Fatal("identity fact changed execution state")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
