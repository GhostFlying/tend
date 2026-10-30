package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// PutAgentCategory appends a global classification revision under owner CAS.
type PutAgentCategory struct{}

func (PutAgentCategory) Operation() string { return "categories.put" }
func (PutAgentCategory) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (PutAgentCategory) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.PutAgentCategoryPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if x.Env.Target.Type != "category" || !protocol.AgentInventoryID(x.Env.Target.ID) || strings.TrimSpace(p.Title) == "" || !slices.Contains([]string{"active", "retired"}, p.State) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid agent category")
	}
	old, err := store.GetAgentCategory(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != old.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "category revision has changed")
	}
	c := protocol.AgentCategory{CategoryID: x.Env.Target.ID, Revision: old.Revision + 1, Title: p.Title, Description: p.Description, State: p.State}
	if err := tx.InsertAgentCategory(ctx, c, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: c, Events: []Event{{ObjectType: "category", ObjectID: c.CategoryID, ObjectVersion: c.Revision, Kind: "category.updated", Body: map[string]any{"state": c.State}}}}, nil
}

func authorizeInventory(ctx context.Context, q store.Querier, p protocol.Principal, r *protocol.RunnerRuntime) error {
	x := &Exec{Principal: p}
	if _, err := requireActiveHost(ctx, x, q); err != nil {
		return err
	}
	if r == nil || !protocol.RunnerProtocolSupported(r.Protocol) || !slices.Contains(r.Capabilities, protocol.CapabilityAgentInventory) {
		return protocol.Errorf(protocol.CodeForbidden, "agent inventory capability is required")
	}
	if err := checkRuntime(ctx, q, p.Subject, r, false); err != nil {
		return err
	}
	current, err := store.GetHostRuntime(ctx, q, p.Subject)
	if err != nil {
		return err
	}
	if !slices.Contains(current.Runtime.Capabilities, protocol.CapabilityAgentInventory) {
		return protocol.Errorf(protocol.CodeForbidden, "agent inventory was not negotiated")
	}
	return nil
}

// RegisterAgentInventory records metadata only, without loading any runtime.
type RegisterAgentInventory struct{}

func (RegisterAgentInventory) Operation() string { return protocol.OpRunnerInventoryRegister }
func (RegisterAgentInventory) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	var p protocol.RegisterAgentInventoryPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return err
	}
	if x.Env.Target.Type != "host" || x.Env.Target.ID != x.Principal.Subject {
		return protocol.Errorf(protocol.CodeForbidden, "inventory belongs to the authenticated host")
	}
	return authorizeInventory(ctx, q, x.Principal, p.Runtime)
}
func (RegisterAgentInventory) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RegisterAgentInventoryPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Projection.FormatVersion == 2 && (p.Runtime == nil || p.Runtime.Protocol < 2) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "format-2 inventory requires runner protocol 2")
	}
	if p.Projection.ExecutionEnvironmentID != x.Principal.Subject || p.LocalRevision < 1 || !protocol.AgentInventoryID(p.SnapshotID) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid inventory identity or version")
	}
	for _, r := range p.Projection.Runtimes {
		if r.Kind == "hosted-manager" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "runner inventory cannot register hosted runtimes")
		}
	}
	canonical, hash, err := protocol.CanonicalAgentInventory(p.Projection)
	if err != nil {
		return Outcome{}, err
	}
	if hash != p.PublicSHA256 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "inventory public hash does not match")
	}
	current, err := store.GetAgentInventory(ctx, tx.Q(), x.Principal.Subject)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != current.Receipt.ServerRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "inventory revision has changed")
	}
	if p.LocalRevision <= current.Receipt.LocalRevision {
		if p.LocalRevision == current.Receipt.LocalRevision && p.SnapshotID == current.Receipt.SnapshotID && hash == current.Receipt.PublicSHA256 && string(canonical) == current.CanonicalJSON {
			return Outcome{Result: current.Receipt}, nil
		}
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "inventory local revision conflicts with accepted metadata")
	}
	categories := []protocol.AgentCategory{}
	seen := map[string]bool{}
	for _, a := range p.Projection.Agents {
		for _, id := range a.Categories {
			if seen[id] {
				continue
			}
			seen[id] = true
			c, err := store.GetAgentCategory(ctx, tx.Q(), id)
			if err != nil {
				return Outcome{}, errNotFound(err, "inventory category does not exist")
			}
			categories = append(categories, c)
		}
	}
	slices.SortFunc(categories, func(a, b protocol.AgentCategory) int { return strings.Compare(a.CategoryID, b.CategoryID) })
	receipt := protocol.AgentInventoryReceipt{HostID: x.Principal.Subject, ServerRevision: current.Receipt.ServerRevision + 1, LocalRevision: p.LocalRevision, SnapshotID: p.SnapshotID, PublicSHA256: hash, RequestID: x.Env.RequestID, State: "registered_staged", Categories: categories}
	row := store.AgentInventoryRow{Receipt: receipt, CanonicalJSON: string(canonical)}
	if err := tx.InsertAgentInventory(ctx, row, *p.Runtime); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: receipt, Events: []Event{{ObjectType: "host", ObjectID: x.Principal.Subject, ObjectVersion: receipt.ServerRevision, Kind: "host.agent_inventory_registered", Body: map[string]any{"local_revision": p.LocalRevision, "public_sha256": hash}}}}, nil
}

// ReadAgentInventory is read-only reconciliation for the current authenticated
// process, including a predecessor's recorded inventory request. It is no heartbeat.
func (a *App) ReadAgentInventory(ctx context.Context, p protocol.Principal, input protocol.AgentInventoryStatusPayload) (protocol.AgentInventoryStatus, error) {
	result := protocol.AgentInventoryStatus{RequestState: "not_requested"}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		if err := authorizeInventory(ctx, tx.Q(), p, input.Runtime); err != nil {
			return err
		}
		catalog, err := readRunnerAgentCatalog(ctx, tx.Q(), p.Subject, a.now().UnixMilli())
		if err != nil {
			return err
		}
		if runtimeSupportsAgentExecution(input.Runtime) {
			result.Executors = catalog.Executors
		}
		result.Online, result.LastHeartbeatAtMillis = catalog.Online, catalog.LastHeartbeatAtMillis
		result.Current, result.Projection = catalog.Current, catalog.Projection
		if input.RequestID != "" {
			result.RequestState = "not_recorded"
			rec, err := store.GetRequest(ctx, tx.Q(), p.DedupDomain(), input.RequestID)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if rec.Operation != protocol.OpRunnerInventoryRegister {
				return protocol.Errorf(protocol.CodeInvalidRequest, "request is not an inventory registration")
			}
			op := operationOf(rec)
			result.RequestPayloadHash = rec.PayloadHash
			result.Request = &op
			result.RequestState = "recorded"
		}
		return nil
	})
	return result, err
}
