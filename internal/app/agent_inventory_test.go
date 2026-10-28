package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func inventoryCategory(t *testing.T, h *harness, id string, revision int64, state string) *protocol.Envelope {
	t.Helper()
	env := h.env("categories.put", protocol.Target{Type: "category", ID: id}, protocol.PutAgentCategoryPayload{Title: id, Description: "Responsibility metadata", State: state}, rev(revision))
	if op := h.exec(owner, env, PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return env
}

func inventoryProjection(t *testing.T, host string) (protocol.AgentInventoryProjection, string) {
	t.Helper()
	cfg := agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "worker", Revision: 1, Kind: agentconfig.AppServerWorker, ExecutableRef: "private-installation", DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{
		{LocalID: "alpha", Revision: 1, DisplayName: "Alpha", RuntimeID: "worker", Categories: []string{"implementer", "reviewer"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "requested-model", Enabled: true},
		{LocalID: "beta", Revision: 1, DisplayName: "Beta", RuntimeID: "worker", Categories: []string{"implementer"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "requested-model", Effort: "high", Enabled: true},
	}}
	d, err := agentconfig.Validate(host, cfg, agentconfig.SourceReference{Kind: agentconfig.LocalFile, Reference: "private-source"})
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := d.CanonicalPublic()
	if err != nil {
		t.Fatal(err)
	}
	return d.Public(), hash
}

func inventorySetup(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.AgentInventoryProjection, string) {
	t.Helper()
	h := newHarness(t)
	inventoryCategory(t, h, "implementer", 0, "active")
	inventoryCategory(t, h, "reviewer", 0, "active")
	r := testRuntime("inventory-first")
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentInventory)
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	p, hash := inventoryProjection(t, "linux-1")
	return h, r, p, hash
}

func inventoryRequest(h *harness, r *protocol.RunnerRuntime, p protocol.AgentInventoryProjection, hash string, local, server int64, snapshot string) *protocol.Envelope {
	return h.env(protocol.OpRunnerInventoryRegister, protocol.Target{Type: "host", ID: "linux-1"}, protocol.RegisterAgentInventoryPayload{Runtime: r, LocalRevision: local, SnapshotID: snapshot, PublicSHA256: hash, Projection: p}, rev(server))
}

func requireInventoryCode(t *testing.T, h *harness, p protocol.Principal, env *protocol.Envelope, code protocol.Code) {
	t.Helper()
	op, err := h.app.Execute(t.Context(), p, env, RegisterAgentInventory{})
	got := protocol.CodeOf(err)
	if err == nil && op.Error != nil {
		got = op.Error.Code
	}
	if got != code {
		t.Fatalf("code=%s operation=%+v err=%v want=%s", got, op, err, code)
	}
}

func TestAgentCategoryOwnerCASAndHistoricalReplay(t *testing.T) {
	h := newHarness(t)
	first := inventoryCategory(t, h, "planner", 0, "active")
	inventoryCategory(t, h, "planner", 1, "retired")
	op := h.exec(owner, first, PutAgentCategory{})
	var old protocol.AgentCategory
	if err := json.Unmarshal(op.Result, &old); err != nil || old.Revision != 1 || old.State != "active" {
		t.Fatalf("historical receipt=%s %v", op.Result, err)
	}
	stale := h.exec(owner, h.env("categories.put", first.Target, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(1)), PutAgentCategory{})
	if stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(stale)
	}
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), first, PutAgentCategory{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("runner edited category", err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		c, err := store.GetAgentCategory(t.Context(), tx.Q(), "planner")
		if err != nil || c.Revision != 2 || c.State != "retired" {
			t.Fatalf("current=%+v %v", c, err)
		}
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM agent_category_versions`).Scan(&n); err != nil || n != 2 {
			t.Fatalf("history=%d %v", n, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentInventoryVersionsReplayAndCurrentProcessStatus(t *testing.T) {
	h, r, p, hash := inventorySetup(t)
	first := inventoryRequest(h, r, p, hash, 1, 0, "snapshot-one")
	one := h.exec(runner("linux-1"), first, RegisterAgentInventory{})
	if one.Status != protocol.OpSucceeded {
		t.Fatal(one)
	}
	h.clock.Advance(61 * time.Second)
	second := inventoryRequest(h, r, p, hash, 2, 1, "snapshot-two")
	two := h.exec(runner("linux-1"), second, RegisterAgentInventory{})
	if two.Status != protocol.OpSucceeded {
		t.Fatal(two)
	}
	inventoryCategory(t, h, "implementer", 1, "retired")
	beforeEvents := h.count("events")
	if replay := h.exec(runner("linux-1"), first, RegisterAgentInventory{}); !bytes.Equal(replay.Result, one.Result) || h.count("events") != beforeEvents {
		t.Fatal("old success re-executed CAS or changed receipt")
	}
	status, err := h.app.ReadAgentInventory(t.Context(), runner("linux-1"), protocol.AgentInventoryStatusPayload{Runtime: r, RequestID: first.RequestID})
	if err != nil || status.Current == nil || status.Current.ServerRevision != 2 || status.Current.LocalRevision != 2 || status.Current.SnapshotID != "snapshot-two" || status.Current.PublicSHA256 != hash || status.Current.State != "registered_staged" {
		t.Fatalf("status=%+v %v", status, err)
	}
	if status.Online || status.LastHeartbeatAtMillis != h.clock.Now().Add(-61*time.Second).UnixMilli() {
		t.Fatal("registration/read refreshed presence", status)
	}
	expectedHash, _ := protocol.PayloadHash(first)
	if status.Request == nil || !bytes.Equal(status.Request.Result, one.Result) || status.RequestPayloadHash != expectedHash || status.Current.Categories[0].Revision != 1 {
		t.Fatal("historical request/category facts changed", status)
	}
	for _, a := range status.Projection.Agents {
		if a.Availability.Available {
			t.Fatal("registration became available")
		}
	}
	if len(status.Projection.Agents) != 2 || len(status.Projection.Agents[0].Categories) != 2 {
		t.Fatal("category membership or agent identity lost")
	}
	// A later revision can retain a retired classification as staged provenance.
	third := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 3, 2, "snapshot-three"), RegisterAgentInventory{})
	var receipt protocol.AgentInventoryReceipt
	if err := json.Unmarshal(third.Result, &receipt); err != nil || receipt.Categories[0].State != "retired" || receipt.State != "registered_staged" {
		t.Fatalf("retired staged category=%s %v", third.Result, err)
	}
	next := testRuntime("inventory-next")
	next.Capabilities = append(next.Capabilities, protocol.CapabilityAgentInventory)
	if op := h.versionedHello("linux-1", next); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), first, RegisterAgentInventory{}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("retired incarnation replayed", err)
	}
	status, err = h.app.ReadAgentInventory(t.Context(), runner("linux-1"), protocol.AgentInventoryStatusPayload{Runtime: next, RequestID: first.RequestID})
	if err != nil || status.Request == nil || status.Request.RequestID != first.RequestID {
		t.Fatal("new process cannot reconcile old receipt", status, err)
	}
	beforeRequests, beforeEvents := h.count("request_records"), h.count("events")
	status, err = h.app.ReadAgentInventory(t.Context(), runner("linux-1"), protocol.AgentInventoryStatusPayload{Runtime: next, RequestID: "req_not_recorded"})
	if err != nil || status.RequestState != "not_recorded" || status.Request != nil || beforeRequests != h.count("request_records") || beforeEvents != h.count("events") {
		t.Fatal("status read wrote state or invented receipt", status, err)
	}
}

func TestAgentInventoryRejectsConflictsAndPrivateFields(t *testing.T) {
	h, r, p, hash := inventorySetup(t)
	first := inventoryRequest(h, r, p, hash, 1, 0, "snapshot-one")
	if op := h.exec(runner("linux-1"), first, RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	requireInventoryCode(t, h, runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 1, "another-snapshot"), protocol.CodeConflictPayload)
	requireInventoryCode(t, h, runner("linux-1"), inventoryRequest(h, r, p, hash, 2, 0, "snapshot-two"), protocol.CodeStaleRevision)
	requireInventoryCode(t, h, runner("linux-1"), inventoryRequest(h, r, p, "wrong-hash", 2, 1, "snapshot-two"), protocol.CodeInvalidRequest)
	changed := *first
	changed.ExpectedControlRevision = rev(1)
	requireInventoryCode(t, h, runner("linux-1"), &changed, protocol.CodeConflictPayload)
	// The same local revision with identical content is a recorded no-op.
	noOp := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 1, "snapshot-one"), RegisterAgentInventory{})
	if !bytes.Equal(noOp.Result, h.exec(runner("linux-1"), first, RegisterAgentInventory{}).Result) {
		t.Fatal("same version no-op changed original receipt")
	}
	private := inventoryRequest(h, r, p, hash, 2, 1, "snapshot-two")
	private.Payload = bytes.Replace(private.Payload, []byte(`"source_kind":"local_file"`), []byte(`"source_kind":"local_file","credentials":"PRIVATE_SENTINEL"`), 1)
	op, err := h.app.Execute(t.Context(), runner("linux-1"), private, RegisterAgentInventory{})
	if protocol.CodeOf(err) != protocol.CodeInvalidRequest || strings.Contains(err.Error(), "PRIVATE_SENTINEL") || op.Status == protocol.OpSucceeded {
		t.Fatal("private field accepted or echoed", op, err)
	}
	hosted, _ := inventoryProjection(t, "linux-1")
	hosted.Runtimes[0].Kind = "hosted-manager"
	requireInventoryCode(t, h, runner("linux-1"), inventoryRequest(h, r, hosted, hash, 2, 1, "snapshot-two"), protocol.CodeInvalidRequest)
	p.Agents[0].Categories = []string{"unknown"}
	_, newHash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	requireInventoryCode(t, h, runner("linux-1"), inventoryRequest(h, r, p, newHash, 2, 1, "snapshot-two"), protocol.CodeNotFound)
	p.Agents[0].Availability.Available = true
	requireInventoryCode(t, h, runner("linux-1"), inventoryRequest(h, r, p, newHash, 2, 1, "snapshot-two"), protocol.CodeInvalidRequest)
	other, _ := inventoryProjection(t, "different-host")
	requireInventoryCode(t, h, runner("linux-1"), inventoryRequest(h, r, other, hash, 2, 1, "snapshot-two"), protocol.CodeInvalidRequest)
	requireInventoryCode(t, h, runner("different-host"), inventoryRequest(h, r, p, hash, 2, 1, "snapshot-two"), protocol.CodeForbidden)
}

func TestAgentInventoryTransactionRollsBackWithoutReceipt(t *testing.T) {
	h, r, p, hash := inventorySetup(t)
	beforeEvents, beforeRequests := h.count("events"), h.count("request_records")
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fail_inventory_receipt BEFORE INSERT ON request_records WHEN NEW.operation='runner.inventory_register' BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := inventoryRequest(h, r, p, hash, 1, 0, "snapshot-one")
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), env, RegisterAgentInventory{}); err == nil {
		t.Fatal("injected receipt failure disappeared")
	}
	if h.count("events") != beforeEvents || h.count("request_records") != beforeRequests || h.count("agent_inventory_versions") != 0 {
		t.Fatal("partial registration escaped transaction")
	}
}

func TestAgentInventoryHelloCompatibilityAndCapabilityGate(t *testing.T) {
	// This is the exact previous response shape, with strict unknown-field decoding.
	type previousHello struct {
		RepositoryBindings bool                    `json:"repository_bindings,omitempty"`
		Control            *protocol.RunnerControl `json:"control,omitempty"`
		LeaseTTLMillis     int64                   `json:"lease_ttl_ms"`
		Runs               []protocol.RunVerdict   `json:"runs"`
		Reassigned         []protocol.Assignment   `json:"reassigned"`
	}
	for _, kind := range []string{"legacy-zero", "old-one", "new-one"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			var r *protocol.RunnerRuntime
			if kind != "legacy-zero" {
				r = testRuntime("hello")
				r.Capabilities = append(r.Capabilities, "unknown-future-capability")
			}
			if kind == "new-one" {
				r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentInventory)
			}
			op := h.versionedHello("linux-1", r)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(op.Result, &fields)
			_, has := fields["agent_inventory"]
			if has != (kind == "new-one") {
				t.Fatalf("ack presence=%v: %s", has, op.Result)
			}
			if kind != "new-one" {
				var previous previousHello
				d := json.NewDecoder(bytes.NewReader(op.Result))
				d.DisallowUnknownFields()
				if err := d.Decode(&previous); err != nil {
					t.Fatal("old strict client rejected hello", err)
				}
				p, hash := inventoryProjection(t, "linux-1")
				requireInventoryCode(t, h, runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "snapshot-one"), protocol.CodeForbidden)
			}
		})
	}
}

func TestAgentInventorySameLocalIdentityOnDifferentHosts(t *testing.T) {
	h, r, p, hash := inventorySetup(t)
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "snapshot-one"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	cfg := testConfig()
	second := cfg.Hosts[0]
	second.HostID = "linux-2"
	cfg.Hosts = append(cfg.Hosts, second)
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r2 := testRuntime("second-host")
	r2.Capabilities = append(r2.Capabilities, protocol.CapabilityAgentInventory)
	if op := h.versionedHello("linux-2", r2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	p2, hash2 := inventoryProjection(t, "linux-2")
	env := h.env(protocol.OpRunnerInventoryRegister, protocol.Target{Type: "host", ID: "linux-2"}, protocol.RegisterAgentInventoryPayload{Runtime: r2, LocalRevision: 1, SnapshotID: "snapshot-one", PublicSHA256: hash2, Projection: p2}, rev(0))
	if op := h.exec(runner("linux-2"), env, RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	one, err := h.app.ReadAgentInventory(t.Context(), runner("linux-1"), protocol.AgentInventoryStatusPayload{Runtime: r})
	if err != nil {
		t.Fatal(err)
	}
	two, err := h.app.ReadAgentInventory(t.Context(), runner("linux-2"), protocol.AgentInventoryStatusPayload{Runtime: r2})
	if err != nil {
		t.Fatal(err)
	}
	if h.count("agent_inventory_versions") != 2 || one.Projection.Agents[0].Identity.LocalID != two.Projection.Agents[0].Identity.LocalID || one.Projection.Agents[0].Identity == two.Projection.Agents[0].Identity {
		t.Fatal("host-local identities collapsed")
	}
}
