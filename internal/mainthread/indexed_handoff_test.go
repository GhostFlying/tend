package mainthread

import (
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIndexHandoffRetryKeepsTargetAndIgnoresUnrelatedLedgerChanges(t *testing.T) {
	f := newHandoffFixture(t)
	hash, err := f.e.m.currentToolsetHash()
	if err != nil {
		t.Fatal(err)
	}
	f.session.injectErr = harness.ErrRejected
	const request = "index-retry"
	if _, err = f.e.m.migrateThread(t.Context(), f.session, f.source, hash, request); !errors.Is(err, harness.ErrRejected) {
		t.Fatal(err)
	}
	job, ok, err := f.e.app.GetHandoff(t.Context(), request)
	if err != nil || !ok || job.Phase != "created" {
		t.Fatal(job, err)
	}
	if err = f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "later-system-observation", Author: "system", Body: strings.Repeat("DO_NOT_COPY_HISTORY", 10000)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target, err := f.e.m.migrateThread(t.Context(), f.session, f.source, hash, request)
	if err != nil || target.ID() != job.TargetThreadID {
		t.Fatal("replaced confirmed target", target, err)
	}
	after, _, err := f.e.app.GetHandoff(t.Context(), request)
	if err != nil || after.Phase != "committed" || after.Prompt != "" || after.SourceVersion != job.SourceVersion {
		t.Fatal("unrelated changes rebuilt index", after, err)
	}
	injections := f.e.h.Injected(target.ID())
	if len(injections) != 1 || injections[0] != job.Prompt || len(injections[0]) > protocol.ContextIndexBudget || strings.Contains(injections[0], "DO_NOT_COPY_HISTORY") {
		t.Fatal("copied or repeated historical prose", injections)
	}
	if _, forks, _ := f.session.counts(); forks != 0 || len(f.e.h.Starts()) != 0 {
		t.Fatal("handoff ran a model turn", forks)
	}
}

func TestLegacyPreparedHandoffReplacedBeforeFirstInjection(t *testing.T) {
	f := newHandoffFixture(t)
	snap, err := f.e.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := f.e.m.currentToolsetHash()
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.e.app.PrepareHandoff(t.Context(), store.ThreadHandoff{Instance: "main", SourceThreadID: f.source.ID(), SourceGeneration: snap.BindingGeneration, SourceVersion: snap.Version, ToolsetHash: hash, Prompt: "LEGACY_LARGE_SUMMARY"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.e.m.migrateThread(t.Context(), f.session, f.source, hash, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(f.e.h.Injected(target.ID()), "\n")
	if strings.Contains(text, "LEGACY_LARGE_SUMMARY") || !strings.Contains(text, "index-v1") {
		t.Fatal("legacy package was injected", text)
	}
}
