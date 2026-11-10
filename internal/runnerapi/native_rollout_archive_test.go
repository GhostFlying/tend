package runnerapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestNativeRolloutArchiveHTTPPersistsOriginalBeforeIndexAndReplays(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	e.createTask()
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "", protocol.ClaimPayload{}))
	if as == nil {
		t.Fatal("no real execution")
	}
	data := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread\"}}\n")
	digest := sha256.Sum256(data)
	p := protocol.NativeRolloutChunk{RunID: as.RunID, SourceID: "source-generation", ThreadID: "thread", Data: data, SHA256: hex.EncodeToString(digest[:])}
	env := e.envelope(protocol.OpRunnerRolloutArchive, as.RunID, p)
	var receipt protocol.NativeRolloutArchiveReceipt
	first := e.mustOK(asHost(), env)
	if err := json.Unmarshal(first, &receipt); err != nil || receipt.NextOffset != int64(len(data)) || receipt.IndexedOffset != int64(len(data)) {
		t.Fatalf("archive receipt=%+v err=%v", receipt, err)
	}
	if again := e.mustOK(asHost(), env); string(again) != string(first) {
		t.Fatal("exact archive request lost its historical receipt")
	}
	var sources, chunks, bindings int
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM retained_native_sources),(SELECT COUNT(*) FROM retained_native_chunks),(SELECT COUNT(*) FROM retained_native_source_runs)`).Scan(&sources, &chunks, &bindings)
	}); err != nil || sources != 1 || chunks != 1 || bindings != 1 {
		t.Fatalf("archive facts=%d/%d/%d err=%v", sources, chunks, bindings, err)
	}
	// The host transport does not grant this internal operation to a worker.
	if status, res := e.call(asRun(*as), e.envelope(protocol.OpRunnerRolloutArchive, as.RunID, p)); status != http.StatusForbidden || res.Error == nil {
		t.Fatalf("Run credential acquired host archive capability: %d %+v", status, res)
	}
}
