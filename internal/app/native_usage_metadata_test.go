package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestNativeUsagePurposeEnrichmentDoesNotQuarantineIdenticalMeasurement(t *testing.T) {
	for _, enrichedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain_first", true: "enriched_first"}[enrichedFirst], func(t *testing.T) {
			h := newHarness(t)
			plain := nativeSample(h.clock.Now().UnixMilli())
			plain.NativeContentHash = "same-immutable-ledger-record"
			enriched := plain
			enriched.Purpose = "issue_owner_native"
			samples := []protocol.TokenUsageSample{plain, enriched, plain, enriched}
			if enrichedFirst {
				samples[0], samples[1] = samples[1], samples[0]
			}
			for _, sample := range samples {
				if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{sample}); err != nil {
					t.Fatal(err)
				}
			}
			read := func() protocol.TokenUsageView {
				t.Helper()
				var v protocol.TokenUsageView
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					v, err = views.TokenUsage(t.Context(), tx.Q(), "total", 0, "", "", 0, h.clock.Now())
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return v
			}
			if v := read(); v.Totals.Requests != 1 || v.Totals.Total != 110 || v.Conflicts != 0 || len(v.Records) != 1 || v.Records[0].Purpose != enriched.Purpose {
				t.Fatal("attribution metadata hid or duplicated native consumption", v)
			}
			plain.Total = usageNumber(999)
			if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{plain}); err != nil {
				t.Fatal(err)
			}
			if v := read(); v.Conflicts != 1 || v.Totals.Requests != 0 {
				t.Fatal("counter contradiction was accepted as metadata", v)
			}
		})
	}
}
