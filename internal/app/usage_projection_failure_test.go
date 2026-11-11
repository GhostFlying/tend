package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_UsageAttributionQueryFailurePreservesDirtyProjection(t *testing.T) {
	h := newHarness(t)
	sample := nativeSample(h.clock.Now().UnixMilli())
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{sample}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		var raw string
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT CAST(dimension_key AS TEXT) FROM token_usage_contributions`).Scan(&raw)
		}); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := read()
	err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		// The missing attribution relation is rolled back with the failed refresh.
		if _, err := tx.Exec(t.Context(), `DROP TABLE main_turns`); err != nil {
			return err
		}
		return refreshUsageProjection(t.Context(), tx)
	})
	if err == nil || !strings.Contains(err.Error(), "main_turns") {
		t.Fatal("attribution read failure became a successful unknown", err)
	}
	if h.count("token_usage_dirty") != 1 || read() != before {
		t.Fatal("failed projection cleared dirty work or replaced the original attribution")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return refreshUsageProjection(t.Context(), tx) }); err != nil {
		t.Fatal(err)
	}
	if h.count("token_usage_dirty") != 0 || read() != before {
		t.Fatal("recovered query did not rebuild the original attribution")
	}
}
