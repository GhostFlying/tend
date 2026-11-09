package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func refreshLocalUsageFixture(t *testing.T, a *App, s *LocalUsageSource) {
	t.Helper()
	all, err := a.LocalUsageSources(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range all {
		if current.ID == s.ID {
			s.Collection, s.ExpectedRevision = current.Collection, current.ExpectedRevision
			if len(s.Cursors) == 0 {
				s.Cursors = current.Cursors
			}
			if s.Coverage == "" {
				s.Coverage = current.Coverage
			}
			if s.WriterStopped && s.Coverage == "caught_up" {
				s.Collection.ReadSucceeded(1, false, true, true, true)
			}
			return
		}
	}
	t.Fatal("usage source not found")
}

func TestLocalUsagePathMoveRetainsIdentityCursorAndStoppedAliases(t *testing.T) {
	h := newHarness(t)
	s := LocalUsageSource{ID: "original", Source: []byte(`{"provider":"codex","thread_id":"thread","session_id":"session","path":"old","writer_id":"writer","no_dispatch_proven":true}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, _ := h.app.LocalUsageSources(t.Context(), "")
	s = all[0]
	s.Cursors = []byte(`{"old":{"offset":123}}`)
	if err := h.app.CommitLocalUsage(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	s.ID = "path-hash-changed"
	s.Source = []byte(`{"provider":"codex","thread_id":"thread","path":"new","writer_id":"writer"}`)
	if err := h.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, _ = h.app.LocalUsageSources(t.Context(), "")
	var cursors map[string]json.RawMessage
	if len(all) != 1 || all[0].ID != "original" || all[0].Collection.State != protocol.UsageDormant {
		t.Fatal("path change created or activated a source", all)
	}
	if err := json.Unmarshal(all[0].Cursors, &cursors); err != nil || string(cursors["old"]) != string(cursors["new"]) {
		t.Fatal("path change lost cursor", all[0], err)
	}
	var metadata struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(all[0].Source, &metadata); err != nil || metadata.SessionID != "session" {
		t.Fatal("path change lost session binding", metadata, err)
	}
	// Retained aliases predate canonical logical identity lookup. Their stopped
	// state must neither block dispatch nor be revived by the next execution.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO token_usage_local_sources(source_key,run_id,conversation_id,source_json,cursors_json,coverage,writer_stopped,collection_json,collection_state,updated_at) SELECT 'old-alias',run_id,conversation_id,source_json,cursors_json,coverage,1,collection_json,'retired',updated_at FROM token_usage_local_sources WHERE source_key='original'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.ActivateLocalUsage(t.Context(), "main", "thread", "new-turn"); err != nil {
		t.Fatal(err)
	}
	all, _ = h.app.LocalUsageSources(t.Context(), "")
	for _, source := range all {
		if source.ID == "original" && source.Collection.State != protocol.UsageActive || source.ID == "old-alias" && !source.WriterStopped {
			t.Fatal("activation changed stopped alias", source)
		}
	}
}

func TestLocalUsageBudgetIsDurableAndRetirementPreservesCoverage(t *testing.T) {
	h := newHarness(t)
	s := LocalUsageSource{ID: "unavailable", Source: []byte(`{"thread_id":"old"}`), WriterStopped: true}
	if err := h.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, _ := h.app.LocalUsageSources(t.Context(), "")
	s = all[0]
	if err := h.app.FailLocalUsage(t.Context(), &s, "history_unavailable"); err != nil {
		t.Fatal(err)
	}
	deadline := s.Collection.RetryDeadlineAt
	h.clock.Advance(5 * time.Minute)
	all, _ = h.app.LocalUsageSources(t.Context(), "")
	s = all[0]
	if s.Collection.RetryDeadlineAt != deadline {
		t.Fatal("deadline was not persisted")
	}
	if err := h.app.FailLocalUsage(t.Context(), &s, "history_unavailable"); err != nil {
		t.Fatal(err)
	}
	all, err := h.app.PendingLocalUsageSources(t.Context(), "")
	if err != nil || len(all) != 0 {
		t.Fatal(all, err)
	}
	all, _ = h.app.LocalUsageSources(t.Context(), "")
	if all[0].Collection.RetirementReason != "unavailable" || all[0].Coverage != "pending" {
		t.Fatal(all[0])
	}
}

func TestRegisteredWriterStopPreservesPositiveEmptyEvidence(t *testing.T) {
	h := newHarness(t)
	s := LocalUsageSource{ID: "unused", Source: []byte(`{"thread_id":"unused","writer_id":"writer","no_dispatch_proven":true}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	s.WriterStopped = true
	if err := h.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, _ := h.app.LocalUsageSources(t.Context(), "")
	if !all[0].WriterStopped || all[0].Collection.RetirementReason != "empty" {
		t.Fatal(all)
	}
}

func TestEmptyUsageRequiresPositiveDispatchEvidenceAndFencesOldEpoch(t *testing.T) {
	h := newHarness(t)
	s := LocalUsageSource{ID: "empty", Source: []byte(`{"thread_id":"thread","no_dispatch_proven":true,"writer_id":"writer"}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if pending, _ := h.app.PendingLocalUsageSources(t.Context(), ""); len(pending) != 0 {
		t.Fatal("metadata activated reading")
	}
	all, _ := h.app.LocalUsageSources(t.Context(), "")
	old := all[0]
	if err := h.app.ActivateLocalUsage(t.Context(), "main", "thread", "original"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.CommitLocalUsage(t.Context(), old, nil); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal(err)
	}
	if err := h.app.StopLocalUsage(t.Context(), "main", ""); err != nil {
		t.Fatal(err)
	}
	all, _ = h.app.LocalUsageSources(t.Context(), "")
	if all[0].Collection.RetirementReason == "empty" {
		t.Fatal("possible dispatch became empty")
	}
	s.ID = "unused"
	s.Source = []byte(`{"thread_id":"unused","no_dispatch_proven":true,"writer_id":"writer"}`)
	if err := h.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := h.app.StopLocalUsage(t.Context(), "main", "unused"); err != nil {
		t.Fatal(err)
	}
	all, _ = h.app.LocalUsageSources(t.Context(), "")
	for _, source := range all {
		if source.ID == "unused" && source.Collection.RetirementReason != "empty" {
			t.Fatal(source)
		}
	}
}
