package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestUsageCollectionControlRetiresWithoutClaimingCompleteAndReplaysOriginal(t *testing.T) {
	h := newHarness(t)
	s := LocalUsageSource{ID: "source", WriterStopped: true, Source: []byte(`{"thread_id":"thread","provider":"codex","session_id":"thread"}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	all, _ := h.app.LocalUsageSources(t.Context(), "")
	revision := all[0].Collection.Revision
	env := h.env(protocol.OpRetireUsageCollection, protocol.Target{Type: "usage_source", ID: s.ID}, protocol.UsageCollectionControl{Reason: "unavailable"}, &revision)
	op := h.exec(owner, env, ControlUsageCollection{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if again := h.exec(owner, env, ControlUsageCollection{}); string(again.Result) != string(op.Result) {
		t.Fatal("request replay changed retirement", again)
	}
	var view protocol.UsageCollectionPage
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		view, err = UsageCollections(t.Context(), tx.Q(), protocol.UsageCollectionQuery{State: protocol.UsageRetired, Reason: "unavailable"}, s.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(view.Sources) != 1 || view.Sources[0].Coverage != "pending" || view.Sources[0].Collection.RetirementReason != "unavailable" {
		t.Fatal(view)
	}
	stale := h.exec(owner, h.env(protocol.OpResumeUsageCollection, env.Target, protocol.UsageCollectionControl{}, &revision), ControlUsageCollection{Resume: true})
	if stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(stale)
	}
	revision = view.Sources[0].ControlRevision
	resumed := h.exec(owner, h.env(protocol.OpResumeUsageCollection, env.Target, protocol.UsageCollectionControl{}, &revision), ControlUsageCollection{Resume: true})
	if resumed.Status != protocol.OpSucceeded {
		t.Fatal(resumed)
	}
	all, _ = h.app.LocalUsageSources(t.Context(), "")
	if all[0].Collection.Epoch <= view.Sources[0].Collection.Epoch || all[0].Coverage != "pending" {
		t.Fatal(all)
	}
}

func TestRunnerCollectionObservationIsNegotiatedMonotonicAndIndependentOfCoverage(t *testing.T) {
	h, as := messageRun(t)
	registration := protocol.NativeUsageRegistration{RunID: as.RunID, ThreadID: "native-thread", SessionID: "native-session", Provider: "codex"}
	reg := h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: "linux-1"}, registration, nil), RegisterNativeUsage{})
	if reg.Status != protocol.OpSucceeded {
		t.Fatal(reg)
	}
	collection := protocol.NewUsageCollection(false, true)
	collection.Stopped(h.clock.Now().UnixMilli(), "exact-writer-close", false)
	collection.RetireUnavailable(h.clock.Now().UnixMilli(), true)
	p := protocol.UsageCollectionObservation{LocalSourceID: "local", RunID: as.RunID, ThreadID: registration.ThreadID, SessionID: registration.SessionID, WriterStopped: true, Collection: collection, Sequence: 1}
	_, denied := h.app.Execute(t.Context(), runner("linux-1"), h.env(protocol.OpRunnerUsageCollection, protocol.Target{Type: "host", ID: "linux-1"}, p, nil), ObserveUsageCollection{})
	if protocol.CodeOf(denied) != protocol.CodeForbidden {
		t.Fatal(denied)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutHostRuntime(t.Context(), "linux-1", store.HostRuntimeRow{Runtime: protocol.RunnerRuntime{Capabilities: []string{protocol.CapabilityUsageCollection}}})
	}); err != nil {
		t.Fatal(err)
	}
	first := h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageCollection, protocol.Target{Type: "host", ID: "linux-1"}, p, nil), ObserveUsageCollection{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	var page protocol.UsageCollectionPage
	read := func() {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			page, err = UsageCollections(t.Context(), tx.Q(), protocol.UsageCollectionQuery{}, as.RunID+"/native-thread")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if page.Sources[0].Coverage != "pending" || page.Sources[0].Collection.RetirementReason != "unavailable" {
		t.Fatal(page)
	}
	old := p
	p.Sequence++
	p.Collection.Reopen("explicit-recovery")
	if op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageCollection, protocol.Target{Type: "host", ID: "linux-1"}, p, nil), ObserveUsageCollection{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	late := h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageCollection, protocol.Target{Type: "host", ID: "linux-1"}, old, nil), ObserveUsageCollection{})
	var result map[string]bool
	_ = json.Unmarshal(late.Result, &result)
	if !result["superseded"] {
		t.Fatal(late)
	}
	read()
	if page.Sources[0].Collection.State != protocol.UsageSettling {
		t.Fatal("old observation overwrote recovery", page)
	}
	revision := page.Sources[0].ControlRevision
	command := h.exec(owner, h.env(protocol.OpRetireUsageCollection, protocol.Target{Type: "usage_source", ID: page.Sources[0].ID}, protocol.UsageCollectionControl{Reason: "unavailable"}, &revision), ControlUsageCollection{})
	if command.Status != protocol.OpSucceeded {
		t.Fatal(command)
	}
	read()
	if len(page.Sources[0].Commands) != 1 || page.Sources[0].Commands[0].Status != "pending" || page.Sources[0].Collection.State != protocol.UsageSettling {
		t.Fatal("offline host was marked applied", page)
	}
}
