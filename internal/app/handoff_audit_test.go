package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHandoffAuditPreservesSourceEventsAndUnknownHistory(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	for _, instance := range []string{"one", "two"} {
		if _, err := h.app.BindMainThread(ctx, instance, instance+"-source", "tools", "private-path"); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.InsertEvent(ctx, store.EventRow{EventID: h.ids.New(protocol.PrefixEvent), Source: "mainthread", ObjectType: "conversation", ObjectID: instance, Kind: "conversation.summary_failed", BodyJSON: `{}`, CreatedAt: h.clock.Now().UnixMilli()})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := h.app.HandoffSnapshot(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	for _, success := range []bool{false, true} {
		if ok, err := h.app.ClaimSummaryAttempt(ctx, snap, 0); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if err := h.app.FinishSummaryAttempt(ctx, snap, success); err != nil {
			t.Fatal(err)
		}
		if err := h.app.FinishSummaryAttempt(ctx, snap, success); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := h.app.HandoffSnapshot(ctx, "one"); err != nil || !snap.Covers(got) {
		t.Fatal("audit changed content coverage", got, err)
	}
	job, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "one", SourceThreadID: snap.ThreadID, SourceVersion: snap.Version, SourceGeneration: snap.BindingGeneration, ToolsetHash: "next", Prompt: "PRIVATE PROMPT"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.PrepareHandoff(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AdvanceHandoff(ctx, job.ID, "prepared", "creating"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.app.RecordHandoffTarget(ctx, job.ID, "one-target", "PRIVATE PATH"); err != nil {
			t.Fatal(err)
		}
	}
	for _, transition := range [][2]string{{"created", "injecting"}, {"injecting", "injected"}} {
		if err := h.app.AdvanceHandoff(ctx, job.ID, transition[0], transition[1]); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := h.app.CommitHandoff(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	all, err := h.app.HandoffAudit(ctx, "one", 0, 200)
	if err != nil || len(all.Items) != 11 || all.More || all.HistoryComplete {
		t.Fatal("unexpected event history", all, err)
	}
	if all.Items[0].Identity != nil || all.Items[0].IdentityObservation != "observation_unavailable" {
		t.Fatal("legacy identity fabricated", all.Items[0])
	}
	starts, completed := 0, 0
	for _, item := range all.Items[1:] {
		if item.Identity == nil || item.Identity.Instance != "one" || item.Identity.SourceThreadID != "one-source" || item.Identity.SourceGeneration != 1 {
			t.Fatal("source rebound to current target", item)
		}
		if item.Kind == "conversation.summary_started" {
			starts++
		}
		if item.Kind == "conversation.handoff_completed" {
			completed++
		}
		if item.Kind == "conversation.handoff_prepared" && item.Identity.TargetThreadID != "" {
			t.Fatal("future target backfilled", item)
		}
		if item.Kind == "conversation.handoff_target" && item.Identity.TargetThreadID != "one-target" {
			t.Fatal(item)
		}
	}
	if starts != 2 || completed != 1 {
		t.Fatal("retry history collapsed or replay duplicated", starts, completed)
	}
	encoded, _ := json.Marshal(all)
	if strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), "private-path") {
		t.Fatal("audit leaked prompt or path")
	}
	var paged []protocol.HandoffAuditEvent
	for after := int64(0); ; {
		page, err := h.app.HandoffAudit(ctx, "one", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page.Items...)
		if !page.More {
			break
		}
		if page.NextAfter <= after {
			t.Fatal("cursor did not progress")
		}
		after = page.NextAfter
	}
	if !reflect.DeepEqual(paged, all.Items) {
		t.Fatal("pagination lost or duplicated facts")
	}
	other, err := h.app.HandoffAudit(ctx, "two", 0, 20)
	if err != nil || len(other.Items) != 1 {
		t.Fatal("instance isolation failed", other, err)
	}
	var sequence int
	var name, path string
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence, &name, &path)
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, path, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	got, err := New(reopened, h.ids, h.clock.Now).HandoffAudit(ctx, "one", 0, 200)
	if err != nil || !reflect.DeepEqual(got, all) {
		t.Fatal("durable audit changed on reopen", got, err)
	}
}

func TestHandoffAuditRejectsInvalidReadsAndCancellation(t *testing.T) {
	h := newHarness(t)
	for _, p := range []struct {
		instance string
		after    int64
		limit    int
	}{{"", 0, 20}, {"main", -1, 20}, {"main", 0, 0}, {"main", 0, 201}} {
		if _, err := h.app.HandoffAudit(t.Context(), p.instance, p.after, p.limit); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatal(p, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.app.HandoffAudit(ctx, "main", 0, 20); err == nil {
		t.Fatal("cancelled read succeeded")
	}
}

func TestHandoffAuditRefreshAndRetargetRecordOnlyActualTransitions(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if _, err := h.app.BindMainThread(ctx, "main", "original", "old-tools", ""); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.app.PrepareHandoff(ctx, store.ThreadHandoff{Instance: "main", SourceThreadID: snap.ThreadID, SourceVersion: snap.Version, SourceGeneration: snap.BindingGeneration, ToolsetHash: "next-tools", Prompt: "original package"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffAudit(ctx, "main", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.app.RetargetPreparedHandoff(ctx, job.ID, "revised-tools", snap.Version, "revised package"); err != nil {
			t.Fatal(err)
		}
	}
	retargeted, err := h.app.HandoffAudit(ctx, "main", before.NextAfter, 200)
	if err != nil || len(retargeted.Items) != 1 || retargeted.Items[0].Kind != "conversation.handoff_retargeted" || retargeted.Items[0].Identity == nil || retargeted.Items[0].Identity.TargetThreadID != "" {
		t.Fatal(retargeted, err)
	}
	if err := h.app.AdvanceHandoff(ctx, job.ID, "prepared", "creating"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordHandoffTarget(ctx, job.ID, "target", ""); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"created", "injecting"}, {"injecting", "injected"}} {
		if err := h.app.AdvanceHandoff(ctx, job.ID, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	prior, err := h.app.HandoffAudit(ctx, "main", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	h.say("New source evidence invalidates the injected package")
	current, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil || current.Version <= snap.Version {
		t.Fatal(current, err)
	}
	for range 2 {
		if err := h.app.RefreshHandoff(ctx, job.ID, current.Version, "fresh package"); err != nil {
			t.Fatal(err)
		}
	}
	refreshed, err := h.app.HandoffAudit(ctx, "main", prior.NextAfter, 200)
	if err != nil || len(refreshed.Items) != 1 {
		t.Fatal(refreshed, err)
	}
	fact := refreshed.Items[0]
	if fact.Kind != "conversation.handoff_refreshed" || fact.Identity == nil || fact.Identity.SourceVersion != current.Version || fact.Identity.Phase != "created" || fact.Identity.TargetThreadID != "target" || fact.Identity.SourceThreadID != "original" {
		t.Fatal("refresh lost exact new state", fact)
	}
	if err := h.app.RefreshHandoff(ctx, job.ID, current.Version+1, "unobserved"); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal(err)
	}
	rejected, err := h.app.HandoffAudit(ctx, "main", refreshed.NextAfter, 200)
	if err != nil || len(rejected.Items) != 0 {
		t.Fatal("rejected refresh appended event", rejected, err)
	}
}
