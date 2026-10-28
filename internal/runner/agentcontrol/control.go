// Package agentcontrol loads a runner's fixed local definitions into its accepted
// registry. It does not load factories, select workers, or publish server inventory.
package agentcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
)

// Installation is private bootstrap data, never a public status field.
type Installation struct {
	RuntimeID       string `json:"runtime_id"`
	ArtifactID      string `json:"artifact_id"`
	ExpectedSHA256  string `json:"expected_sha256"`
	AdapterID       string `json:"adapter_id"`
	AdapterRevision string `json:"adapter_revision"`
	LocalReference  string `json:"local_reference"`
}

type Config struct {
	SourceFile    string         `json:"source_file"`
	Installations []Installation `json:"installations"`
}

type Options struct {
	Directory     string
	EnvironmentID string
	Config        Config
	Registry      agentregistry.Options
}

var errSourceUnreadable = errors.New("agent source cannot be read")
var errHostedRuntime = errors.New("hosted definitions are not runner configurations")

// Control owns the process's sole registry writer. CLI clients use its handler.
type Control struct {
	changes       chan struct{}
	registry      *agentregistry.Registry
	environment   string
	source        string
	installations []agentregistry.InstallationReference
}

// Open creates an empty registry only when its directory does not exist. Source
// changes are accepted solely by explicit reload, including on the first start.
func Open(ctx context.Context, options Options) (*Control, error) {
	if !filepath.IsAbs(options.Config.SourceFile) || !filepath.IsAbs(options.Directory) {
		return nil, errors.New("agent control requires absolute bootstrap paths")
	}
	var registry *agentregistry.Registry
	_, err := os.Stat(options.Directory)
	if errors.Is(err, os.ErrNotExist) {
		registry, err = agentregistry.Create(ctx, options.Directory, options.EnvironmentID, options.Registry)
	} else if err == nil {
		registry, err = agentregistry.Open(ctx, options.Directory, options.EnvironmentID, options.Registry)
	}
	if err != nil {
		return nil, err
	}
	control := &Control{changes: make(chan struct{}, 1), registry: registry, environment: options.EnvironmentID, source: options.Config.SourceFile}
	for _, p := range options.Config.Installations {
		control.installations = append(control.installations, agentregistry.InstallationReference{RuntimeID: p.RuntimeID, ArtifactID: p.ArtifactID, ExpectedSHA256: p.ExpectedSHA256, AdapterID: p.AdapterID, AdapterRevision: p.AdapterRevision, LocalReference: p.LocalReference})
	}
	return control, nil
}

func (c *Control) Close() error { return c.registry.Close() }

func (c *Control) Status(ctx context.Context) (protocol.LocalAgentConfiguration, error) {
	obs, err := c.registry.Current(ctx)
	if err != nil {
		return protocol.LocalAgentConfiguration{}, err
	}
	status := protocol.LocalAgentConfiguration{State: "empty", Revision: obs.LastConfirmedRevision, SnapshotID: obs.LastConfirmedSnapshot.ID, PublicSHA256: obs.PublicHash, PendingRequestID: obs.PendingRequestID}
	if obs.LastConfirmedRevision > 0 {
		status.State = "local_accepted"
		snapshot, err := c.registry.Resolve(ctx, obs.LastConfirmedSnapshot)
		if err != nil {
			return status, err
		}
		var p protocol.AgentInventoryProjection
		if err := json.Unmarshal(snapshot.PublicBytes(), &p); err != nil {
			return status, err
		}
		status.Projection = &p
	}
	return status, nil
}

func (c *Control) candidate(ctx context.Context) (*agentregistry.Candidate, *protocol.AgentInventoryProjection, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(c.source)
	if err != nil {
		return nil, nil, errSourceUnreadable
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	cfg, err := agentconfig.Decode(raw)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range cfg.Runtimes {
		if r.Kind == agentconfig.HostedManager {
			return nil, nil, errHostedRuntime
		}
	}
	source := agentconfig.SourceReference{Kind: agentconfig.LocalFile, Reference: c.source}
	candidate, err := agentregistry.Prepare(c.environment, cfg, source, slices.Clone(c.installations))
	if err != nil {
		return nil, nil, err
	}
	definitions, err := agentconfig.Validate(c.environment, cfg, source)
	if err != nil {
		return nil, nil, err
	}
	projection := definitions.Public()
	return candidate, &projection, nil
}

func (c *Control) Validate(ctx context.Context) (protocol.AgentInventoryProjection, error) {
	_, p, err := c.candidate(ctx)
	if err != nil {
		return protocol.AgentInventoryProjection{}, err
	}
	return *p, nil
}

func (c *Control) Reload(ctx context.Context, input protocol.ReloadLocalAgentsRequest) (protocol.LocalAgentConfigurationReceipt, error) {
	candidate, _, err := c.candidate(ctx)
	if err != nil {
		return protocol.LocalAgentConfigurationReceipt{}, err
	}
	receipt, err := c.registry.Commit(ctx, agentregistry.CommitRequest{RequestID: input.RequestID, ExpectedRevision: input.ExpectedRevision, Candidate: candidate})
	if err != nil {
		return protocol.LocalAgentConfigurationReceipt{}, err
	}
	c.notifyAccepted()
	return publicReceipt(receipt), nil
}

func (c *Control) Reconcile(ctx context.Context, requestID string) (string, *protocol.LocalAgentConfigurationReceipt, error) {
	outcome, err := c.registry.Reconcile(ctx, requestID)
	if outcome.Receipt == nil {
		return outcome.State, nil, err
	}
	if err == nil {
		c.notifyAccepted()
	}
	receipt := publicReceipt(*outcome.Receipt)
	return outcome.State, &receipt, err
}

func publicReceipt(r agentregistry.Receipt) protocol.LocalAgentConfigurationReceipt {
	return protocol.LocalAgentConfigurationReceipt{RequestID: r.RequestID, Revision: r.Revision, SnapshotID: r.Snapshot.ID, PublicSHA256: r.PublicHash}
}

// Changes wakes the existing runner reconciler; accepted state remains in registry.
func (c *Control) Changes() <-chan struct{} { return c.changes }

func (c *Control) notifyAccepted() {
	select {
	case c.changes <- struct{}{}:
	default:
	}
}
