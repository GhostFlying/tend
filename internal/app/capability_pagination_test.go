package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestCapabilityCandidatePaginationRetainsCurrentAndInFlightPins(t *testing.T) {
	h, r, retained := capabilitySetup(t)
	task := capabilityTask(t, h)
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
	var result claimResult
	if err := json.Unmarshal(op.Result, &result); err != nil || op.Status != protocol.OpSucceeded || result.Assignment.TaskID != task || result.Assignment.Execution == nil || *result.Assignment.Execution != retained {
		t.Fatal("original declaration was not claimed", op, err)
	}
	var inv store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inv, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inv.Projection.Agents[0].Revision++
	inv.Projection.Agents[0].Capabilities = nil
	_, hash, err := protocol.CanonicalAgentInventory(inv.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, inv.Projection, hash, 2, 1, "updated-alpha"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for i := range r.AgentExecutors {
		p := &r.AgentExecutors[i].Pin
		p.InventoryRevision = 2
		p.SnapshotID = "updated-alpha"
		p.PublicSHA256 = hash
		if p.Identity == retained.Identity {
			p.AgentRevision++
		}
	}
	r.AgentContinuations = []protocol.AgentExecutor{{Pin: retained, Available: true}}
	r.ContinuationBackend = "git"
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentContinuation)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	pending := capabilityTask(t, h)
	all, err := h.app.TaskCapabilityExplanationPage(t.Context(), pending, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 100})
	if err != nil || len(all.Candidates) != 3 {
		t.Fatal("expected current alpha/beta and retained alpha", all, err)
	}
	want := map[protocol.RunExecutionPin]protocol.AgentCapabilityView{}
	for _, candidate := range all.Candidates {
		if candidate.Pin == nil {
			t.Fatal("missing candidate pin", candidate)
		}
		want[*candidate.Pin] = candidate
		if *candidate.Pin == retained {
			if len(candidate.Declared) != 1 || candidate.Current.State != "eligible" {
				t.Fatal("retained declaration lost", candidate)
			}
		} else if candidate.Pin.Identity == retained.Identity && (len(candidate.Declared) != 0 || candidate.Current.State != "unavailable") {
			t.Fatal("current revision inherited original declaration", candidate)
		}
	}
	if len(want) != 3 {
		t.Fatal("expected three distinct pins", want)
	}
	for _, limit := range []int{1, 2} {
		got := map[protocol.RunExecutionPin]protocol.AgentCapabilityView{}
		after := ""
		for pages := 0; ; pages++ {
			if pages > len(want) {
				t.Fatal("pagination did not terminate")
			}
			page, err := h.app.TaskCapabilityExplanationPage(t.Context(), pending, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: limit, After: after})
			if err != nil || len(page.Candidates) > limit || page.AsOf != all.AsOf {
				t.Fatal("page failed or watermark changed", page, err)
			}
			for _, candidate := range page.Candidates {
				if candidate.Pin == nil {
					t.Fatal("missing paged pin", candidate)
				}
				if _, duplicate := got[*candidate.Pin]; duplicate {
					t.Fatal("duplicate pin across pages", candidate.Pin)
				}
				got[*candidate.Pin] = candidate
			}
			if page.Next == "" {
				break
			}
			after = page.Next
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("limit=%d: paged %d candidates, unpaged %d; missing or changed frozen pin", limit, len(got), len(want))
		}
	}
	first, err := h.app.TaskCapabilityExplanationPage(t.Context(), pending, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 1})
	if err != nil || first.Next == "" {
		t.Fatal(first, err)
	}
	if _, err := h.app.TaskCapabilityExplanationPage(t.Context(), pending, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 1, After: first.Next, State: "eligible"}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("cursor accepted changed filter", err)
	}
	// Change a declaration fact, rather than merely the heartbeat: task
	// cursors seal their projected facts, not every unrelated host observation.
	r.AgentContinuations = nil
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.TaskCapabilityExplanationPage(t.Context(), pending, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 1, After: first.Next}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("cursor accepted changed candidate facts", err)
	}
}

func TestCapabilityCandidatePaginationRejectsPreviousCursorFormat(t *testing.T) {
	h, _, _ := capabilitySetup(t)
	task := capabilityTask(t, h)
	var after string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		now := h.clock.Now().UnixMilli()
		p, err := views.TaskCapabilities(t.Context(), tx.Q(), task, 1, protocol.RoleImplement, now)
		if err != nil {
			return err
		}
		signature, err := json.Marshal(p)
		if err != nil {
			return err
		}
		clock, err := store.CapabilityCursor([]any{task, int64(1), protocol.RoleImplement, protocol.AgentCapabilityQuery{}}, "", now)
		if err != nil {
			return err
		}
		key, err := store.CapabilityCursor(string(signature), "", now)
		if err != nil {
			return err
		}
		after = clock.Encode(key.Encode("linux-1/alpha/worker/git"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.TaskCapabilityExplanationPage(t.Context(), task, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 1, After: after}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("old ordering cursor must require a fresh page", err)
	}
}
