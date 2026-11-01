package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

func managerPurpose(e *entry) bool {
	return e.ownerInput != nil && e.ownerInput.Runtime.Enabled && e.input.Agent.Enabled
}

func (p *Pool) probeManager(ctx context.Context, e *entry) bool {
	if !managerPurpose(e) {
		return false
	}
	if p.options.ProbeOwner != nil {
		return p.options.ProbeOwner(ctx, *e.ownerInput) == nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "tend-coordination-probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	s, err := traex.Start(ctx, traex.Options{Bin: e.ownerInput.Runtime.ExecutableRef, Dir: dir, HostedCoordination: true, Log: p.options.Log})
	if err != nil {
		return false
	}
	defer s.Close()
	model := e.input.Agent.Model
	config := coordination.Isolation()
	models, err := s.Models(ctx)
	if err != nil {
		return false
	}
	for _, candidate := range models {
		if candidate.ID == model || candidate.Name == model {
			model = candidate.Name
			if candidate.Provider != "" {
				config["model_provider"] = candidate.Provider
			}
			break
		}
	}
	if e.input.Agent.Effort != "" {
		config["model_reasoning_effort"] = e.input.Agent.Effort
	}
	thread, err := s.StartThread(ctx, harness.ThreadConfig{Cwd: dir, Model: model, Sandbox: "read-only", ApprovalPolicy: "never", Ephemeral: true, Config: config})
	if err != nil {
		return false
	}
	permissions, ok := thread.(harness.PermissionedThread)
	if !ok {
		return false
	}
	effective := permissions.PermissionDefaults()
	return effective.Sandbox == "read-only" && effective.ApprovalPolicy == "never"
}

func (p *Pool) ResolveManager(ctx context.Context, pin protocol.RunExecutionPin) (runner.ManagerExecution, error) {
	if pin.Purpose != "issue_manager" || pin.BackendID != "" || pin.Validate(pin.Identity.ExecutionEnvironmentID) != nil {
		return runner.ManagerExecution{}, errors.New("manager pin is invalid")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, err := p.options.Source.ResolveAccepted(ctx, pin.SnapshotID)
	if err != nil {
		return runner.ManagerExecution{}, err
	}
	public, hash, err := snapshotPublic(snapshot.PublicBytes())
	if err != nil || hash != pin.PublicSHA256 || public.ExecutionEnvironmentID != pin.Identity.ExecutionEnvironmentID {
		return runner.ManagerExecution{}, errors.New("original manager snapshot differs")
	}
	entries, err := p.load(ctx, pin.SnapshotID)
	if err != nil {
		return runner.ManagerExecution{}, err
	}
	e := entries[pin.Identity.LocalID]
	if e == nil || !managerPurpose(e) || e.input.Agent.Revision != pin.AgentRevision || e.ownerInput.Runtime.ID != pin.Runtime.RuntimeID || e.ownerInput.Runtime.Revision != pin.Runtime.Revision || e.input.Agent.Model != pin.Profile.Model || e.input.Agent.Effort != pin.Profile.Effort || !p.probeManager(ctx, e) {
		return runner.ManagerExecution{}, errors.New("original manager executor is unavailable")
	}
	bin := e.ownerInput.Runtime.ExecutableRef
	return runner.ManagerExecution{Instructions: e.input.Agent.Instructions, Profile: pin.Profile, Open: func(ctx context.Context, dir string) (harness.Session, error) {
		return traex.Start(ctx, traex.Options{Bin: bin, Dir: dir, HostedCoordination: true, Log: p.options.Log})
	}}, nil
}

func snapshotPublic(raw []byte) (protocol.AgentInventoryProjection, string, error) {
	var public protocol.AgentInventoryProjection
	if err := json.Unmarshal(raw, &public); err != nil {
		return public, "", err
	}
	_, hash, err := protocol.CanonicalAgentInventory(public)
	return public, hash, err
}
