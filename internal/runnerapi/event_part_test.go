package runnerapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_EventOriginalSurvivesInterruptedTransferAndFinalReceiptLoss(t *testing.T) {
	e, as, _, _ := httpMessageRun(t)
	body, err := json.Marshal(map[string]string{"output": strings.Repeat("界<", 600000)})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(protocol.EventsPayload{Events: []protocol.RunEvent{{Seq: 77, Kind: "command.completed", NativeID: "original-command", Body: body}}})
	if err != nil || len(payload) <= maxBody {
		t.Fatal("expected oversized payload", len(payload), err)
	}
	sum := sha256.Sum256(payload)
	part := protocol.RunEventPart{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload)), Operation: protocol.OpRunnerSyncEvents}
	send := func(offset int64) protocol.RunEventPartReceipt {
		t.Helper()
		part.Offset = offset
		part.Data = payload[offset:min(offset+protocol.RunEventPartBytes, part.Size)]
		var result protocol.RunEventPartReceipt
		err := json.Unmarshal(e.mustOK(asRun(as), e.envelope(protocol.OpRunnerEventPart, as.RunID, part)), &result)
		if err != nil || result.SHA256 != part.SHA256 || result.Next <= offset || result.Stored != (result.Next == part.Size) {
			t.Fatal(result, err)
		}
		return result
	}
	first := send(0)
	e.mustOK(asRun(as), e.envelope(protocol.OpRunsSubmitArtifact, as.RunID, protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{{Repo: "tend", Path: "tend", BaseSHA: baseSHA, Branch: as.Workspace.Branch, CommitSHA: commitSHA, Changed: true}}}))
	before := receiptExecutionSnapshot(t, e, as)
	if got := send(0); got != first {
		t.Fatalf("partial receipt changed: %+v %+v", first, got)
	}
	// After losing the final receipt, a fresh sender starts from byte zero.
	for range 2 {
		for offset := int64(0); offset < part.Size; {
			offset = send(offset).Next
		}
	}
	if after := receiptExecutionSnapshot(t, e, as); !reflect.DeepEqual(before, after) {
		t.Fatal("late event changed terminal execution")
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, ok, err := store.RunLogAt(t.Context(), tx.Q(), as.RunID, 77)
		if err == nil && (!ok || row.NativeID != "original-command" || row.BodyJSON != string(body)) {
			t.Error("original did not round trip")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	part.Operation = protocol.OpRunsReportStarted
	if status, _ := e.call(asRun(as), e.envelope(protocol.OpRunnerEventPart, as.RunID, part)); status != http.StatusForbidden {
		t.Fatalf("unexpected operation admitted: %d", status)
	}
}
