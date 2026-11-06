//go:build linux

package updater

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func boundedServerTestData(h *harness) *protocol.BoundedDataIdentity {
	device, inode, err := BoundedDataFileIdentity(h.data)
	if err != nil {
		panic(err)
	}
	return &protocol.BoundedDataIdentity{Path: h.data, Device: device, Inode: inode, Version: h.cfg.StorageVersion}
}

func TestBoundedServerRejectsUnboundLedgerBeforeStop(t *testing.T) {
	for _, kind := range []string{"missing", "other_path", "replaced_before_authorization", "replaced_before_stop", "replaced_before_backup", "replaced_before_start"} {
		t.Run(kind, func(t *testing.T) {
			h, p, _ := committedFixture(t)
			auth := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF1")
			auth.DataIdentity = boundedServerTestData(h)
			replace := func() {
				t.Helper()
				if err := os.Rename(h.data, h.data+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(h.data, []byte("different ledger inode"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "missing":
				auth.DataIdentity = nil
			case "other_path":
				auth.DataIdentity.Path += ".other"
			case "replaced_before_authorization":
				replace()
			}
			before := h.s.Status()
			err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "force")
			if kind == "missing" || kind == "other_path" || kind == "replaced_before_authorization" {
				if err == nil || h.p.stops != 0 || h.p.starts != 0 || h.s.Status().Generation != before.Generation {
					t.Fatal("unbound ledger authority changed state or admitted replacement", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			steps := map[string]int{"replaced_before_stop": 0, "replaced_before_backup": 1, "replaced_before_start": 2}[kind]
			for range steps {
				if err := h.s.Step(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			replace()
			stops := h.p.stops
			if err := h.s.Step(t.Context()); err == nil || h.p.starts != 0 || h.p.stops != stops {
				t.Fatal("changed ledger reached the next destructive transition", err)
			}
		})
	}
}

func TestBoundedServerOrdinaryApplyCannotEscalateUnknownStop(t *testing.T) {
	h, p, a := committedFixture(t)
	if err := h.s.PrepareCommittedMaintenance(t.Context(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
	a.archive.HostedDrained = true
	a.archive.Hosts[0].Runtime.Mode = protocol.RunnerReady
	process := &boundedHungStop{&receiptCrashProcess{fakeProcess: h.p}}
	h.s.opts.Process = process
	auth := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF1")
	auth.DataIdentity = boundedServerTestData(h)
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, auth, "apply"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Step(t.Context()); !errors.Is(err, ErrUnknown) {
		t.Fatal("graceful stop must remain unconfirmed", err)
	}
	boundedHistoryAdvance(h, auth.Timeouts.StopMillis+1)
	if err := h.s.Step(t.Context()); err == nil || process.crashes != 0 || h.p.starts != 0 {
		t.Fatal("ordinary authority escalated an unknown stop without explicit force", err)
	}
}

func TestBoundedServerPlanCapturesFixedLedgerIdentity(t *testing.T) {
	h, p, _ := committedFixture(t)
	root := t.TempDir()
	planFile, authFile := filepath.Join(root, "plan.json"), filepath.Join(root, "authorization.json")
	auth := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF1")
	auth.DataIdentity = &protocol.BoundedDataIdentity{Path: h.data + ".wrong", Device: 1, Inode: 1, Version: 1}
	if err := writeJSON(planFile, CommittedHandoffPlan{Maintenance: p}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(authFile, auth); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatal(err)
	}
	out, err := BoundedCommand(t.Context(), h.cfg, "plan", planFile, authFile, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var planned struct {
		Authorization protocol.BoundedMaintenanceAuthorization `json:"authorization"`
	}
	if err := json.Unmarshal(raw, &planned); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(planned.Authorization.DataIdentity, boundedServerTestData(h)) {
		t.Fatal("plan did not capture the fixed local ledger's actual identity")
	}
	after, err := os.ReadFile(authFile)
	if err != nil || string(before) != string(after) {
		t.Fatal("planning mutated the independent authorization input", err)
	}
	if _, err := BoundedCommand(t.Context(), h.cfg, "force", planFile, authFile, auth.RequestID, auth.ExpectedGeneration, auth.ExpectedPhase); err == nil || h.p.stops != 0 || h.p.starts != 0 {
		t.Fatal("command admitted an unbound ledger before handoff", err)
	}
}

func TestBoundedServerExtensionPreservesLedgerIdentityAcrossMigration(t *testing.T) {
	h, p, _ := committedFixture(t)
	first := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF1")
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, first, "force"); err != nil {
		t.Fatal(err)
	}
	next := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF2")
	next.DataIdentity.Version++
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, next, "extend"); err != nil {
		t.Fatal("schema migration must retain the same physical ledger", err)
	}
	if err := os.Rename(h.data, h.data+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.data, []byte("a different ledger"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := boundedHistoryAuthorization(h, p, "req_01M6F01JSS6Q9BFV6DJNSSBHF3")
	before := h.s.Status()
	if err := h.s.AuthorizeBoundedMaintenance(t.Context(), p, changed, "extend"); err == nil || h.s.Status().Generation != before.Generation || h.p.starts != 0 || h.p.stops != 0 {
		t.Fatal("a fresh budget silently replaced the original ledger lineage", err)
	}
}
