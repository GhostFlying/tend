package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
)

func TestUpgradeLogsRetainFirstErrorAndOnlyDurableTransitions(t *testing.T) {
	h := newHarness(t)
	var out bytes.Buffer
	h.s.opts.Log = slog.New(slog.NewJSONHandler(&out, nil))
	h.s.state.Operation = &Operation{ID: "original-operation", Binding: Binding{UpgradeID: "batch"}, Phase: "stopping", LastError: "exact original stop error", StartedAt: h.now}
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	if record["upgrade_id"] != "batch" || record["phase"] != "stopping" || record["error"] != "exact original stop error" {
		t.Fatal("durable failure lost identity or reason", record)
	}
	out.Reset()
	if err := h.s.save(); err != nil || out.Len() != 0 {
		t.Fatal("unchanged polling duplicated a transition", err, out.String())
	}
	h.s.state.Operation.Phase = "committed"
	h.s.writeState = func(string, State) error { return errors.New("disk unavailable") }
	if err := h.s.save(); err == nil || out.Len() != 0 {
		t.Fatal("failed persistence fabricated a commit log", err, out.String())
	}
}
