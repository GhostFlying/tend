package app

import (
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"testing"
	"time"
)

func usageNumber(n int64) *int64 { return &n }
func TestRequestUsageDeduplicatesTransportAndCorrectsOriginalWindow(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "usage", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	occurred := h.clock.Now().Add(-time.Hour).UnixMilli()
	sample := protocol.TokenUsageSample{Source: "native/thread", RequestID: "response-1", ThreadID: "thread", TurnID: "turn", Revision: 1, Settlement: "provisional", Profile: "test", Model: "model-a", OccurredAt: &occurred, Input: usageNumber(100), CacheRead: usageNumber(60), CacheWrite: usageNumber(10), Output: usageNumber(20), Total: usageNumber(120)}
	upload := func(seq int64) {
		t.Helper()
		raw, _ := json.Marshal(sample)
		_, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: seq, Kind: "usage.request", Body: raw}})
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func(window string) protocol.TokenUsageView {
		t.Helper()
		var v protocol.TokenUsageView
		err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			v, err = views.TokenUsage(t.Context(), tx.Q(), window, 0, "", "", 0, h.clock.Now())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	upload(1)
	upload(2)
	if v := read("total"); v.Totals.Requests != 1 || v.Totals.Total != 120 || v.Totals.Input != 100 {
		t.Fatalf("double counted request/cache: %+v", v)
	}
	sample.Revision = 2
	sample.Settlement = "final"
	sample.Total = usageNumber(110)
	sample.Output = usageNumber(10)
	sample.Model = "model-b"
	upload(3)
	sample.Revision = 1
	sample.Settlement = "provisional"
	sample.Total = usageNumber(120)
	sample.Output = usageNumber(20)
	sample.Model = "model-a"
	upload(4)
	v := read("total")
	if v.Totals.Total != 110 || v.Totals.Final != 1 || len(v.Models) != 1 || v.Models[0].Name != "model-b" {
		t.Fatalf("correction/order: %+v", v)
	}
	// Receipt arrival is in another natural period, while corrected consumption
	// remains at its native occurrence instant.
	h.clock.Advance(48 * time.Hour)
	sample.Revision = 3
	sample.Settlement = "final"
	sample.Model = "model-b"
	sample.Total = usageNumber(105)
	sample.Output = usageNumber(5)
	upload(5)
	if v := read("day"); v.Totals.Requests != 0 {
		t.Fatalf("correction moved to arrival period: %+v", v)
	}
	if v := read("total"); v.Totals.Total != 105 {
		t.Fatal(v)
	}
}

func TestRequestUsageUnknownTimeFieldsAndConflictRemainExplicit(t *testing.T) {
	h := newHarness(t)
	s := protocol.TokenUsageSample{Source: "native/thread", RequestID: "request", ThreadID: "thread", Revision: 1, Settlement: "final", Profile: "test", Input: usageNumber(0), Total: usageNumber(5)}
	if err := h.app.RecordTokenUsage(t.Context(), "main", s); err != nil {
		t.Fatal(err)
	}
	read := func(kind string) protocol.TokenUsageView {
		var v protocol.TokenUsageView
		err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			v, err = views.TokenUsage(t.Context(), tx.Q(), kind, 0, "", "", 0, h.clock.Now())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := read("day")
	if v.Totals.Requests != 0 || v.Unattributed.Requests != 1 || v.Unattributed.InputKnown != 1 || v.Unattributed.CacheWriteKnown != 0 {
		t.Fatal(v)
	}
	v = read("total")
	if v.Totals.Total != 5 || v.Totals.CacheWriteKnown != 0 || v.Models[0].Name != "实际模型未提供" {
		t.Fatal(v)
	}
	s.Total = usageNumber(99)
	if err := h.app.RecordTokenUsage(t.Context(), "main", s); err != nil {
		t.Fatal(err)
	}
	v = read("total")
	if v.Conflicts != 1 || v.Totals.Requests != 0 {
		t.Fatalf("conflicting fact silently replaced current reading: %+v", v)
	}
}

func TestRunRolloutWindowsPreserveIdentityAndSequence(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "rollout", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	var batch []protocol.RunEvent
	for n := int64(1); n <= 250; n++ {
		batch = append(batch, protocol.RunEvent{Seq: n, Kind: "item.completed", Body: json.RawMessage(`{"type":"agentMessage","text":"hello"}`)})
	}
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, batch); err != nil {
		t.Fatal(err)
	}
	read := func(before, after int64) protocol.RunRolloutPage {
		var v protocol.RunRolloutPage
		err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			v, err = views.RunRollout(t.Context(), tx.Q(), as.RunID, before, after)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	tail := read(0, 0)
	if len(tail.Logs) != 100 || tail.Before != 151 || tail.After != 250 || !tail.MoreBefore || tail.MoreAfter || tail.RunID != as.RunID || tail.Logs[0].CreatedAt.IsZero() {
		t.Fatal(tail)
	}
	older := read(tail.Before, 0)
	if older.Before != 51 || older.After != 150 || !older.MoreAfter {
		t.Fatal(older)
	}
	forward := read(0, older.After)
	if forward.Before != 151 || forward.After != 250 {
		t.Fatal(forward)
	}
	empty := read(0, 250)
	if len(empty.Logs) != 0 || empty.After != 250 || empty.Watermark != 250 {
		t.Fatal(empty)
	}
}

func TestRequestUsageIdenticalPayloadWithDifferentOwnershipIsAConflict(t *testing.T) {
	h := newHarness(t)
	sample := protocol.TokenUsageSample{Source: "native/thread", RequestID: "response", ThreadID: "thread", Revision: 1, Settlement: "final", Profile: "test", Total: usageNumber(120)}
	if err := h.app.RecordTokenUsage(t.Context(), "main", sample); err != nil {
		t.Fatal(err)
	}
	h.createTask(owner, createTaskPayload{Title: "ownership", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	raw, _ := json.Marshal(sample)
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "usage.request", Body: raw}}); err != nil {
		t.Fatal(err)
	}
	var v protocol.TokenUsageView
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		v, err = views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if v.Conflicts != 1 || v.Totals.Requests != 0 || h.count("token_usage_facts") != 2 {
		t.Fatal("conflicting provenance was lost", v)
	}
}

func TestRequestUsageStaleRevisionConflictIsOrderIndependent(t *testing.T) {
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			h := newHarness(t)
			base := protocol.TokenUsageSample{Source: "native/thread", RequestID: "request", ThreadID: "thread", Revision: 1, Settlement: "provisional", Total: usageNumber(10)}
			conflicting := base
			conflicting.Total = usageNumber(11)
			newer := base
			newer.Revision, newer.Settlement, newer.Total = 2, "final", usageNumber(20)
			facts := []protocol.TokenUsageSample{base, conflicting, newer}
			for _, index := range order {
				if err := h.app.RecordTokenUsage(t.Context(), "main", facts[index]); err != nil {
					t.Fatal(err)
				}
			}
			// Neither a duplicate nor a higher revision may erase a known conflict.
			if err := h.app.RecordTokenUsage(t.Context(), "main", base); err != nil {
				t.Fatal(err)
			}
			newer.Revision = 3
			if err := h.app.RecordTokenUsage(t.Context(), "main", newer); err != nil {
				t.Fatal(err)
			}
			var v protocol.TokenUsageView
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				v, err = views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if v.Conflicts != 1 || v.Totals.Requests != 0 || h.count("token_usage_facts") != 4 {
				t.Fatalf("order %v changed conflict accounting: %+v", order, v)
			}
		})
	}
}
