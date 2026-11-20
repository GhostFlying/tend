package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFault_ManagerConnectionEndsBeforeThreadRepeatClaim(t *testing.T) {
	h, rt, _, as, _ := reviewManagerAssignment(t)
	event := protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"sequence":0,"thread_id":"","disconnected":true}`)}
	h.runAs(as, protocol.OpManagerNativeEvent, event, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, as.RunID) != 1 {
		t.Fatal("premature consumption without a stop")
	}
	h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "transport ended before start", LastSequence: 1}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	rt.Sequence++
	res := reviewClaimManager(t, h, rt)
	if res.RunID == as.RunID || res.TaskID != as.TaskID || h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=1`, as.RunID) != 1 {
		t.Fatal("connection end kept original task from continuing", res)
	}
}
