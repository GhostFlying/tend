package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAgentCatalogCategoryPagesUseLatestRevision(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"alpha", "beta", "gamma"} {
		inventoryCategory(t, h, id, 0, "active")
	}
	inventoryCategory(t, h, "beta", 1, "retired")
	first, err := h.app.AgentCategories(t.Context(), "", "", 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].CategoryID != "alpha" || !first.More || first.Next != "alpha" {
		t.Fatal(first, err)
	}
	next, err := h.app.AgentCategories(t.Context(), "active", first.Next, 1)
	if err != nil || len(next.Items) != 1 || next.Items[0].CategoryID != "gamma" || next.More || next.Next != "" {
		t.Fatal("retired category resurfaced or cursor skipped boundary", next, err)
	}
	retired, err := h.app.AgentCategories(t.Context(), "retired", "", 0)
	if err != nil || len(retired.Items) != 1 || retired.Items[0].CategoryID != "beta" || retired.Items[0].Revision != 2 {
		t.Fatal(retired, err)
	}
	all, err := h.app.AgentCategories(t.Context(), "all", "", 100)
	if err != nil || len(all.Items) != 3 || all.Items[1] != retired.Items[0] {
		t.Fatal(all, err)
	}
	for _, input := range []struct {
		state string
		limit int
	}{{"unknown", 1}, {"all", 101}, {"active", -1}} {
		if _, err := h.app.AgentCategories(t.Context(), input.state, "", input.limit); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatal("invalid filter accepted", input, err)
		}
	}
}

func TestAgentCatalogSeparatesRegistrationExecutorsAndPresence(t *testing.T) {
	h, runtime, projection, hash := inventorySetup(t)
	empty, err := h.app.RunnerAgentCatalog(t.Context(), "linux-1")
	if err != nil || !empty.Online || empty.Current != nil || empty.Projection != nil || len(empty.Executors) != 0 {
		t.Fatal("hello fabricated registration", empty, err)
	}
	if _, err := h.app.RunnerAgentCatalog(t.Context(), "missing"); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, runtime, projection, hash, 1, 0, "catalog-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution)
	runtime.JournalVersion = 12
	a := projection.Agents[0]
	runtime.AgentExecutors = []protocol.AgentExecutor{{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "catalog-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}}}
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	requests, events, outbox := h.count("request_records"), h.count("events"), h.count("outbox")
	catalog, err := h.app.RunnerAgentCatalog(t.Context(), "linux-1")
	if err != nil || !catalog.Online || catalog.Current.State != "registered_staged" || catalog.Current.PublicSHA256 != hash || !reflect.DeepEqual(catalog.Executors, runtime.AgentExecutors) || catalog.Projection.Agents[0].Availability.Available {
		t.Fatal("registration or actual observation overwritten", catalog, err)
	}
	status, err := h.app.ReadAgentInventory(t.Context(), runner("linux-1"), protocol.AgentInventoryStatusPayload{Runtime: runtime})
	if err != nil || !reflect.DeepEqual(status.Executors, catalog.Executors) || !reflect.DeepEqual(status.Current, catalog.Current) || status.Online != catalog.Online || status.LastHeartbeatAtMillis != catalog.LastHeartbeatAtMillis {
		t.Fatal("runner public facts drifted", status, catalog, err)
	}
	body, _ := json.Marshal(catalog)
	for _, private := range []string{"private-source", "private-installation", "token_hash", "request_state", "request_payload_hash"} {
		if strings.Contains(string(body), private) {
			t.Fatal("private or reconciliation detail exposed", private)
		}
	}
	h.clock.Advance(RunnerOfflineAfter)
	stale, err := h.app.RunnerAgentCatalog(t.Context(), "linux-1")
	if err != nil || stale.Online || !reflect.DeepEqual(stale.Executors, catalog.Executors) || stale.LastHeartbeatAtMillis != catalog.LastHeartbeatAtMillis {
		t.Fatal("reading renewed presence or erased last observation", stale, err)
	}
	if h.count("request_records") != requests || h.count("events") != events || h.count("outbox") != outbox {
		t.Fatal("catalog read wrote business state")
	}
	h.withHosts("linux-2")
	retired, err := h.app.RunnerAgentCatalog(t.Context(), "linux-1")
	if err != nil || retired.Online || retired.Current.SnapshotID != catalog.Current.SnapshotID || !reflect.DeepEqual(retired.Executors, catalog.Executors) {
		t.Fatal("retirement hid history or became online", retired, err)
	}
}
