package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRetainedNativeCommandsAndRepliesReturnOriginalBodies(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	ctx := t.Context()
	payload := json.RawMessage(`{"thread_id":"original-thread","configuration":"` + strings.Repeat("中文😀", 1000) + `"}`)
	id, err := h.app.QueueManagerNative(ctx, as.RunID, "models", payload)
	if err != nil {
		t.Fatal(err)
	}
	claimed := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	if claimed.Status != protocol.OpSucceeded {
		t.Fatal(claimed)
	}
	var command protocol.ManagerNativeRequest
	if err = json.Unmarshal(claimed.Result, &command); err != nil {
		t.Fatal(err)
	}
	if command.ID != id || string(command.Payload) != string(payload) {
		t.Fatal("claim changed original command", command.ID)
	}
	models := json.RawMessage(`[{"id":"original-model","description":"` + strings.Repeat("model details ", 2000) + `"}]`)
	reply := protocol.ManagerNativeReply{ID: id, Result: models}
	env := h.env(protocol.OpManagerNativeReply, protocol.Target{Type: "run", ID: as.RunID}, reply, nil)
	principal := runPrincipal(as.RunID, as.Role)
	original, err := h.app.Execute(ctx, principal, env, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if err != nil || original.Status != protocol.OpSucceeded {
		t.Fatal(original, err)
	}
	replay, err := h.app.Execute(ctx, principal, env, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if err != nil || string(replay.Result) != string(original.Result) {
		t.Fatal("reply retry changed original receipt", err)
	}
	result, ready, err := h.app.ManagerNativeResult(ctx, as.RunID, id)
	if err != nil || !ready || !reflect.DeepEqual(result, reply) {
		t.Fatal("full models result lost", ready, err)
	}
	if _, err = h.app.QueueManagerNative(ctx, as.RunID, "close", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	closing, err := h.app.ManagerClosingAttachment(ctx, as.RunID, "models", "")
	if err != nil || string(closing) != string(models) {
		t.Fatal("closing attachment lost models array", err)
	}
	if err = h.store.Read(ctx, func(tx *store.ReadTx) error {
		var requestBytes, resultBytes int
		if err := tx.Q().QueryRowContext(ctx, `SELECT length(payload_json),length(result_json) FROM manager_native_requests WHERE request_id=?`, id).Scan(&requestBytes, &resultBytes); err != nil {
			return err
		}
		if requestBytes > 1000 || resultBytes > 1000 {
			t.Fatal("native bodies copied into ledger", requestBytes, resultBytes)
		}
		for _, request := range []string{id, claimed.RequestID, env.RequestID} {
			record, err := store.GetRequest(ctx, tx.Q(), map[bool]string{true: "system", false: principal.DedupDomain()}[request == id], request)
			if err != nil {
				return err
			}
			if !json.Valid([]byte(record.ResultJSON)) {
				t.Fatal("stored receipt no longer valid JSON")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOriginalStorageControlRetryDoesNotAdvanceAnotherBatch(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	body := json.RawMessage(`{"type":"sample","text":"` + strings.Repeat("original ", 400) + `"}`)
	for range 2 {
		if err := h.app.RecordRolloutEvent(ctx, "main", "thread", "turn", "item", "item_completed", body); err != nil {
			t.Fatal(err)
		}
	}
	env := h.env(protocol.OpMigrateOriginals, protocol.Target{Type: "storage", ID: "originals"}, protocol.OriginalMigration{Limit: 1}, rev(1))
	first := h.exec(owner, env, OriginalStorageControl{OperationName: protocol.OpMigrateOriginals})
	before, err := h.app.OriginalStorageStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second := h.exec(owner, env, OriginalStorageControl{OperationName: protocol.OpMigrateOriginals})
	after, err := h.app.OriginalStorageStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != protocol.OpSucceeded || string(first.Result) != string(second.Result) || before.Revision != after.Revision || before.MigratedRows != after.MigratedRows {
		t.Fatal("original retry advanced storage migration", first, second, before, after)
	}
}

func TestOriginalNativeArchiveHostReceiptRetryAndEventReuse(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	ctx := t.Context()
	var host string
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		run, err := store.GetRun(ctx, tx.Q(), as.RunID)
		host = run.HostID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Native originals remain archival facts after a Run stops; auth remains tied
	// to the real execution host, independent of worker lease or display state.
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET observed_state='stopped',desired_state='stop' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(`{"type":"item_completed","text":"` + strings.Repeat("native original ", 500) + `"}`)
	data := []byte(`{"payload":` + string(body) + "}\n")
	chunk := protocol.NativeRolloutChunk{RunID: as.RunID, SourceID: "native-generation", ThreadID: "native-thread", Data: data, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	env := h.env(protocol.OpRunnerRolloutArchive, protocol.Target{Type: "run", ID: as.RunID}, chunk, nil)
	principal := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: host}
	first, err := h.app.Execute(ctx, principal, env, ArchiveNativeRollout{})
	if err != nil || first.Status != protocol.OpSucceeded {
		t.Fatal(first, err)
	}
	second, err := h.app.Execute(ctx, principal, env, ArchiveNativeRollout{})
	if err != nil || string(first.Result) != string(second.Result) {
		t.Fatal("archive request retry changed receipt", err)
	}
	var receipt protocol.NativeRolloutArchiveReceipt
	if err = json.Unmarshal(first.Result, &receipt); err != nil || receipt.NextOffset != int64(len(data)) || receipt.IndexedOffset != receipt.NextOffset {
		t.Fatal(receipt, err)
	}
	// The normal hosted-main observation path consumes the already indexed body.
	if err = h.app.RecordRolloutEvent(ctx, "main", "native-thread", "turn", "item", "item_completed", body); err != nil {
		t.Fatal(err)
	}
	if err = h.store.Read(ctx, func(tx *store.ReadTx) error {
		var archives int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(DISTINCT archive_id) FROM retained_original_bodies`).Scan(&archives); err != nil {
			return err
		}
		if archives != 1 {
			t.Fatalf("event created another fragment original: %d", archives)
		}
		var raw string
		if err := tx.Q().QueryRowContext(ctx, `SELECT data_json FROM agent_rollout_events ORDER BY seq DESC LIMIT 1`).Scan(&raw); err != nil {
			return err
		}
		restored, err := store.ResolveJSON(ctx, tx.Q(), raw)
		if err == nil && restored != string(body) {
			t.Fatal("event source changed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	foreign := h.env(protocol.OpRunnerRolloutArchive, env.Target, chunk, nil)
	_, err = h.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "foreign-host"}, foreign, ArchiveNativeRollout{})
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("foreign archive err=%v", err)
	}
}
