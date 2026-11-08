// Package agentexec resolves named workers from accepted local snapshots.
package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/harness/remote"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
)

// Runtime.ExecutableRef selects the native command. Installation remains frozen
// local provenance and never overrides that requested executable.
type BuildInput struct {
	Environment  *codexenv.Binding
	Runtime      agentconfig.RuntimeBinding
	Agent        agentconfig.AgentDefinition
	Installation agentregistry.InstallationReference
}

type Options struct {
	Extensions       *extensions.Agent
	EnvironmentHomes string

	Environments *codexenv.Bindings
	CentralOrca  *orca.Client
	ProbeCentral func(context.Context) error

	Source        *agentcontrol.Control
	BackendID     string
	Workspaces    runner.Workspaces
	BridgeCommand string
	IDs           protocol.IDGen
	Now           func() time.Time
	Log           *slog.Logger
	// Probe is read-only adapter setup; Build creates the existing harness factory.
	Probe func(context.Context, BuildInput) error
	// ProbeOwner independently verifies the coordination adapter. Nil uses the
	// actual native read-only/never permission probe, without a model turn.
	ProbeOwner func(context.Context, BuildInput) error
	Build      func(BuildInput) (harness.Workers, error)
}

type entry struct {
	managedWorkerReady     map[string]bool
	managedOwnerReady      map[string]bool
	input                  BuildInput
	ownerInput             *BuildInput
	workers                harness.Workers
	reason                 string
	managerReady           bool
	environmentUnavailable bool
}
type Pool struct {
	managedWorkers   map[string]harness.Workers
	managedOwners    map[string]bool
	retainedMu       sync.Mutex
	retained         []protocol.RunExecutionPin
	retainedObserved []protocol.AgentExecutor
	options          Options
	mu               sync.Mutex
	snapshots        map[string]map[string]*entry
}

func New(options Options) (*Pool, error) {
	if options.Source == nil || options.Workspaces == nil || (options.BackendID != "git" && options.BackendID != "orca") {
		return nil, errors.New("named execution needs its local source and explicit workspace backend")
	}
	p := &Pool{options: options, snapshots: map[string]map[string]*entry{}, managedWorkers: map[string]harness.Workers{}, managedOwners: map[string]bool{}}
	if p.options.Probe == nil {
		p.options.Probe = p.probe
	}
	if p.options.Build == nil {
		p.options.Build = p.build
	}
	return p, nil
}
func (p *Pool) load(ctx context.Context, id string) (map[string]*entry, error) {
	if saved := p.snapshots[id]; saved != nil {
		return saved, nil
	}
	snapshot, err := p.options.Source.ResolveAccepted(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, installs, err := snapshot.Configuration()
	if err != nil {
		return nil, err
	}
	runtimes := map[string]agentconfig.RuntimeBinding{}
	installation := map[string]agentregistry.InstallationReference{}
	for _, r := range cfg.Runtimes {
		runtimes[r.ID] = r
	}
	for _, i := range installs {
		installation[i.RuntimeID] = i
	}
	entries := map[string]*entry{}
	for _, a := range cfg.Agents {
		r := runtimes[a.RuntimeID]
		e := &entry{input: BuildInput{Runtime: r, Agent: a, Installation: installation[r.ID]}}
		ownerRuntime := r
		if a.OwnerRuntimeID != "" {
			ownerRuntime = runtimes[a.OwnerRuntimeID]
		}
		if ownerRuntime.Kind == agentconfig.AppServerWorker && (slices.Contains(a.Purposes, agentconfig.IssueManager) || ownerRuntime.CLI != "" && slices.Contains(a.Purposes, agentconfig.WorkerRun)) {
			input := BuildInput{Runtime: ownerRuntime, Agent: a, Installation: installation[ownerRuntime.ID]}
			e.ownerInput = &input
		}
		switch {
		case !a.Enabled || !r.Enabled:
			e.reason = "definition_disabled"
		case r.Kind == agentconfig.HostedManager:
			e.reason = "hosted_runtime_not_supported"
		case (r.Kind == agentconfig.OrcaWorker || r.Kind == agentconfig.CentralWorker) && p.options.BackendID != "orca":
			e.reason = "workspace_backend_not_supported"
		default:
			e.reason = "adapter_not_loaded"
		}
		entries[a.LocalID] = e
	}
	p.snapshots[id] = entries
	return entries, nil
}
func (p *Pool) Observe(ctx context.Context) ([]protocol.AgentExecutor, error) {
	status, err := p.options.Source.Status(ctx)
	if err != nil {
		return nil, err
	}
	if status.Revision == 0 {
		return nil, nil
	}
	if !p.mu.TryLock() {
		return nil, nil
	}
	defer p.mu.Unlock()
	entries, err := p.load(ctx, status.SnapshotID)
	if err != nil {
		return nil, err
	}
	out := []protocol.AgentExecutor{}
	for _, a := range status.Projection.Agents {
		e := entries[a.Identity.LocalID]
		if e == nil {
			continue
		}
		worker := protocol.WorkerAppServer
		if e.input.Runtime.Kind == agentconfig.OrcaWorker || e.input.Runtime.Kind == agentconfig.CentralWorker {
			worker = protocol.WorkerOrca
		}
		pin := protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: int64(status.Revision), SnapshotID: status.SnapshotID, PublicSHA256: status.PublicSHA256, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: worker, BackendID: p.options.BackendID}
		if slices.Contains(e.input.Agent.Purposes, agentconfig.WorkerRun) {
			reason := e.reason
			if e.environmentUnavailable && (reason == "" || canProbe(reason)) {
				reason = "environment_unavailable"
			}
			out = append(out, protocol.AgentExecutor{Pin: pin, Available: reason == "" && e.workers != nil, Reason: reason})
		}
		if managerPurpose(e) {
			pin.Purpose = "issue_manager"
			pin.Worker = protocol.WorkerAppServer
			pin.Runtime = protocol.AgentRuntimeReference{RuntimeID: e.ownerInput.Runtime.ID, Revision: e.ownerInput.Runtime.Revision}
			pin.BackendID = ""
			reason := ""
			if !e.managerReady {
				reason = "manager_isolation_not_verified"
			}
			out = append(out, protocol.AgentExecutor{Pin: pin, Available: e.managerReady, Reason: reason})
		}
	}
	if p.options.Extensions != nil {
		expanded := []protocol.AgentExecutor{}
		for _, observation := range out {
			if !p.options.Extensions.Consumer.Enabled(observation.Pin.Runtime.RuntimeID) {
				expanded = append(expanded, observation)
				continue
			}
			for _, a := range status.Projection.Agents {
				if a.Identity != observation.Pin.Identity {
					continue
				}
				for _, category := range a.Categories {
					row := observation
					pin, ready := p.options.Extensions.Current(row.Pin.Runtime.RuntimeID, category, a.Identity)
					row.Pin.Extensions = pin
					if row.Pin.Purpose == "issue_manager" {
						row.Available = ready && entries[a.Identity.LocalID].managedOwnerReady[category]
					} else {
						row.Available = ready && entries[a.Identity.LocalID].managedWorkerReady[category]
					}
					if !ready {
						row.Available = false
						row.Reason = "managed_extensions_not_ready"
					} else if !row.Available {
						if row.Pin.Purpose == "issue_manager" {
							row.Reason = "manager_isolation_not_verified"
						} else {
							row.Reason = "adapter_unavailable"
						}
					}
					expanded = append(expanded, row)
				}
			}
		}
		out = expanded
	}
	return out, nil
}
func (p *Pool) Resolve(ctx context.Context, pin protocol.RunExecutionPin) (runner.ResolvedExecution, error) {
	if err := pin.Validate(pin.Identity.ExecutionEnvironmentID); err != nil {
		return runner.ResolvedExecution{}, err
	}
	if pin.BackendID != p.options.BackendID {
		return runner.ResolvedExecution{}, errors.New("original workspace backend is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, err := p.options.Source.ResolveAccepted(ctx, pin.SnapshotID)
	if err != nil {
		return runner.ResolvedExecution{}, err
	}
	var public protocol.AgentInventoryProjection
	// PublicBytes is the same canonical safe projection used by registration.
	if err := json.Unmarshal(snapshot.PublicBytes(), &public); err != nil {
		return runner.ResolvedExecution{}, err
	}
	_, hash, err := protocol.CanonicalAgentInventory(public)
	if err != nil || hash != pin.PublicSHA256 || public.ExecutionEnvironmentID != pin.Identity.ExecutionEnvironmentID {
		return runner.ResolvedExecution{}, errors.New("original execution snapshot identity differs")
	}
	entries, err := p.load(ctx, pin.SnapshotID)
	if err != nil {
		return runner.ResolvedExecution{}, err
	}
	e := entries[pin.Identity.LocalID]
	if e != nil && (canProbe(e.reason)) {
		if p.options.Environments.Resolve(e.input.Runtime.ID) != nil || e.input.Runtime.Kind == agentconfig.AppServerWorker && e.input.Runtime.CLI == "codex" {
			// Availability probes use current mappings. Native Codex recovery
			// restores the saved run binding at startup even after its mapping
			// was removed, so it must not depend on probing the new default home.
			e.workers, err = p.options.Build(e.input)
			if err != nil {
				return runner.ResolvedExecution{}, err
			}
		} else {
			p.initialize(ctx, e)
		}
	}
	// A native refusal removes future availability, but original sessions still
	// need this exact factory for stop and recovery.
	if e == nil || e.workers == nil {
		return runner.ResolvedExecution{}, errors.New("original named worker adapter is unavailable")
	}
	a, r := e.input.Agent, e.input.Runtime
	worker := protocol.WorkerAppServer
	if r.Kind == agentconfig.OrcaWorker || r.Kind == agentconfig.CentralWorker {
		worker = protocol.WorkerOrca
	}
	if a.Revision != pin.AgentRevision || r.ID != pin.Runtime.RuntimeID || r.Revision != pin.Runtime.Revision || a.Model != pin.Profile.Model || a.Effort != pin.Profile.Effort || worker != pin.Worker {
		return runner.ResolvedExecution{}, errors.New("original named execution configuration differs")
	}
	profile := pin.Profile
	if pin.Extensions != (protocol.ExtensionExecutionPin{}) {
		if p.options.Extensions == nil {
			return runner.ResolvedExecution{}, errors.New("original managed extension cache is unavailable")
		}
		binding, err := p.options.Extensions.Binding(ctx, pin.Extensions, r.ID)
		if err != nil {
			return runner.ResolvedExecution{}, err
		}
		input := e.input
		input.Environment = binding
		key := managedWorkerKey(input, pin.Extensions)
		workers := p.managedWorkers[key]
		if workers == nil {
			workers, err = p.options.Build(input)
			if err != nil {
				return runner.ResolvedExecution{}, err
			}
			p.managedWorkers[key] = workers
		}
		return runner.ResolvedExecution{DeclaredCapabilities: slices.Clone(a.Capabilities), Workers: workers, Workspaces: p.options.Workspaces, Instructions: a.Instructions, Profile: &profile}, nil
	}
	return runner.ResolvedExecution{DeclaredCapabilities: slices.Clone(a.Capabilities), Workers: e.workers, Workspaces: p.options.Workspaces, Instructions: a.Instructions, Profile: &profile}, nil
}

// Reject records a concrete native refusal for future selection, not a fallback.
func (p *Pool) Reject(pin protocol.RunExecutionPin, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entries := p.snapshots[pin.SnapshotID]; entries != nil {
		if e := entries[pin.Identity.LocalID]; e != nil {
			e.reason = reason
		}
	}
}
func (p *Pool) probe(ctx context.Context, input BuildInput) error {
	if p.environment(input) != nil && (input.Runtime.Kind != agentconfig.AppServerWorker || input.Runtime.CLI != "codex") {
		return errors.New("strict environment binding requires a native Codex app-server runtime")
	}
	if _, err := exec.LookPath(input.Runtime.ExecutableRef); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if input.Runtime.Kind == agentconfig.CentralWorker {
		if runtime.GOOS == "windows" {
			return errors.New("central native terminal needs a supported shell adapter")
		}
		if p.options.CentralOrca == nil || p.options.ProbeCentral == nil {
			return errors.New("central native runtime is not configured")
		}
		return p.options.ProbeCentral(ctx)
	}
	if input.Runtime.Kind == agentconfig.OrcaWorker {
		client := &orca.Client{Bin: input.Runtime.ExecutableRef}
		if err := client.CheckWorkspaceProtocol(ctx); err != nil {
			return err
		}
		if input.Runtime.CLI == "traex" {
			return client.CheckTraeModelSelection(ctx)
		}
		return nil
	}
	session, err := traex.Start(ctx, traex.Options{Bin: input.Runtime.ExecutableRef, Log: p.options.Log, Environment: p.environment(input)})
	if err != nil {
		return err
	}
	return session.Close()
}
func (p *Pool) build(input BuildInput) (harness.Workers, error) {
	if p.environment(input) != nil && (input.Runtime.Kind != agentconfig.AppServerWorker || input.Runtime.CLI != "codex") {
		return nil, errors.New("strict environment binding requires a native Codex app-server runtime")
	}
	switch input.Runtime.Kind {
	case agentconfig.CentralWorker:
		if p.options.CentralOrca == nil {
			return nil, errors.New("central worker needs its native terminal client")
		}
		return remote.NewWorkers(remote.WorkerOptions{Bin: input.Runtime.ExecutableRef, Helper: p.options.BridgeCommand, Orca: p.options.CentralOrca, Model: input.Agent.Model, Effort: input.Agent.Effort, Log: p.options.Log}), nil
	case agentconfig.AppServerWorker:
		return traex.NewWorkers(traex.WorkerOptions{EnvironmentHomes: p.options.EnvironmentHomes, Session: traex.Options{Bin: input.Runtime.ExecutableRef, Log: p.options.Log, Environment: p.environment(input)}, Model: input.Agent.Model, Effort: input.Agent.Effort, StrictResume: true}), nil
	case agentconfig.OrcaWorker:
		agent := protocol.AgentCodex
		if input.Runtime.CLI == "traex" {
			agent = protocol.AgentTraex
		}
		return orca.NewWorkers(orca.WorkerOptions{Client: &orca.Client{Bin: input.Runtime.ExecutableRef}, Agent: agent, Model: input.Agent.Model, Effort: input.Agent.Effort, TerminalSurface: true, BridgeCommand: p.options.BridgeCommand, BridgeArgs: []string{"worker-mcp"}, IDs: p.options.IDs, Now: p.options.Now, Log: p.options.Log}), nil
	}
	return nil, errors.New("unsupported named worker adapter")
}

func (p *Pool) initialize(ctx context.Context, e *entry) error {
	if err := p.options.Probe(ctx, e.input); err != nil {
		e.reason = "adapter_unavailable"
		if errors.Is(err, orca.ErrTraeModelSelectionUnsupported) {
			e.reason = "orca_trae_model_selection_not_supported"
		}
		return err
	}
	workers, err := p.options.Build(e.input)
	if err != nil {
		e.reason = "adapter_configuration_not_supported"
		return nil
	}
	e.workers = workers
	e.reason = ""
	return nil
}

// Refresh is called by the existing runner reconciler, never its heartbeat.
func (p *Pool) Refresh(ctx context.Context) error {
	retainedErr := p.refreshRetained(ctx)
	if p.options.Extensions != nil {
		defer p.options.Extensions.RefreshAdoptions(ctx)
	}
	status, err := p.options.Source.Status(ctx)
	if err != nil {
		return err
	}
	if status.Revision == 0 {
		return retainedErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := p.load(ctx, status.SnapshotID)
	if err != nil {
		return err
	}
	var retry error
	for _, e := range entries {
		if handled, err := p.refreshManaged(ctx, e, status.Projection.ExecutionEnvironmentID); handled {
			retry = errors.Join(retry, err)
			continue
		}

		ownerReady := true
		if e.ownerInput != nil {
			if err := p.environmentReady(ctx, e.ownerInput.Runtime.ID); err != nil {
				e.managerReady, ownerReady = false, false
				retry = errors.Join(retry, err)
			}
		}
		if managerPurpose(e) && ownerReady && !e.managerReady {
			e.managerReady = p.probeManager(ctx, e)
			if !e.managerReady {
				retry = errors.Join(retry, errors.New("owner native isolation is not verified"))
			}
		}
		if err := p.environmentReady(ctx, e.input.Runtime.ID); err != nil {
			e.environmentUnavailable = true
			retry = errors.Join(retry, err)
			continue
		}
		e.environmentUnavailable = false
		if canProbe(e.reason) {
			retry = errors.Join(retry, p.initialize(ctx, e))
		}
	}
	return errors.Join(retainedErr, retry)
}

func canProbe(reason string) bool {
	return reason == "adapter_not_loaded" || reason == "adapter_unavailable" || reason == "orca_trae_model_selection_not_supported"
}

func (p *Pool) environmentReady(ctx context.Context, runtime string) error {
	if binding := p.options.Environments.Resolve(runtime); binding != nil {
		_, err := binding.Pin(ctx, "")
		return err
	}
	return nil
}

// Close stops only the factories owned by this process, including held loops.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, workers := range p.managedWorkers {
		if closer, ok := workers.(interface{ Close() }); ok {
			closer.Close()
		}
	}
	for _, entries := range p.snapshots {
		for _, e := range entries {
			if closer, ok := e.workers.(interface{ Close() }); ok {
				closer.Close()
			}
		}
	}
}

func (p *Pool) environment(input BuildInput) *codexenv.Binding {
	if input.Environment != nil {
		return input.Environment
	}
	return p.options.Environments.Resolve(input.Runtime.ID)
}
func managedWorkerKey(input BuildInput, pin protocol.ExtensionExecutionPin) string {
	raw, _ := json.Marshal(struct {
		Input BuildInput
		Pin   protocol.ExtensionExecutionPin
	}{input, pin})
	return extensions.Hash(raw)
}
