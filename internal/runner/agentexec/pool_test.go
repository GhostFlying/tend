package agentexec_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/testkit"
)

type sourceFixture struct {
	control  *agentcontrol.Control
	config   agentconfig.Config
	source   string
	sequence uint64
}

func newSource(t *testing.T) *sourceFixture {
	t.Helper()
	root := t.TempDir()
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 721)
	source := filepath.Join(root, "source.json")
	control, err := agentcontrol.Open(t.Context(), agentcontrol.Options{Directory: filepath.Join(root, "registry"), EnvironmentID: "runner-a", Config: agentcontrol.Config{SourceFile: source, Installations: []agentcontrol.Installation{{RuntimeID: "native", ArtifactID: "installation", ExpectedSHA256: "sha256:" + strings.Repeat("a", 64), AdapterID: "appserver", AdapterRevision: "1", LocalReference: "PRIVATE_INSTALLATION"}}}, Registry: agentregistry.Options{Now: clock.Now, NewSnapshotID: func() string { return "snapshot-" + ids.New(protocol.PrefixRequest) }}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	return &sourceFixture{control: control, source: source, config: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "native", Revision: 1, Kind: agentconfig.AppServerWorker, ExecutableRef: "PRIVATE_INSTALLATION", DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "worker", Revision: 1, DisplayName: "Worker", RuntimeID: "native", Categories: []string{"implementer"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "model", Instructions: "PRIVATE_ROLE", Enabled: true}}}}
}
func (f *sourceFixture) reload(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.source, data, 0600); err != nil {
		t.Fatal(err)
	}
	f.sequence++
	if _, err = f.control.Reload(t.Context(), protocol.ReloadLocalAgentsRequest{RequestID: "req_" + strings.Repeat("0", 25) + string(rune('A'+f.sequence)), ExpectedRevision: f.sequence - 1}); err != nil {
		t.Fatal(err)
	}
}
func newPool(t *testing.T, f *sourceFixture, probe func(context.Context, agentexec.BuildInput) error) *agentexec.Pool {
	t.Helper()
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Probe: probe, Build: func(agentexec.BuildInput) (harness.Workers, error) { return &testkit.Workers{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func observed(t *testing.T, p *agentexec.Pool) protocol.AgentExecutor {
	t.Helper()
	o, err := p.Observe(t.Context())
	if err != nil || len(o) != 1 {
		t.Fatalf("observations=%+v err=%v", o, err)
	}
	return o[0]
}
func TestColdResolveUsesOriginalPrivateSnapshotAndRejectedFactoryRemainsRecoverable(t *testing.T) {
	f := newSource(t)
	f.reload(t)
	p := newPool(t, f, func(context.Context, agentexec.BuildInput) error { return nil })
	if err := p.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := observed(t, p)
	if !first.Available {
		t.Fatal(first)
	}
	f.config.Agents[0].Instructions = "CHANGED_PRIVATE_ROLE"
	f.reload(t)
	// The public hash intentionally excludes instructions; the private snapshot does not.
	next := observed(t, p)
	if first.Pin.PublicSHA256 != next.Pin.PublicSHA256 || first.Pin.SnapshotID == next.Pin.SnapshotID {
		t.Fatal("private/public identity conflated", first, next)
	}
	cold := newPool(t, f, func(context.Context, agentexec.BuildInput) error { return nil })
	original, err := cold.Resolve(t.Context(), first.Pin)
	if err != nil || original.Instructions != "PRIVATE_ROLE" {
		t.Fatal(original, err)
	}
	current, err := cold.Resolve(t.Context(), next.Pin)
	if err != nil || current.Instructions != "CHANGED_PRIVATE_ROLE" {
		t.Fatal(current, err)
	}
	cold.Reject(next.Pin, "adapter_rejected_configuration")
	if o := observed(t, cold); o.Available || o.Reason != "adapter_rejected_configuration" {
		t.Fatal(o)
	}
	recovered, err := cold.Resolve(t.Context(), next.Pin)
	if err != nil || recovered.Workers != current.Workers {
		t.Fatal("refusal lost original cleanup factory", err)
	}
	missing := first.Pin
	missing.SnapshotID = "snapshot-unaccepted"
	if _, err := cold.Resolve(t.Context(), missing); err == nil {
		t.Fatal("missing snapshot silently defaulted")
	}
	changed := first.Pin
	changed.Profile.Model = "other-model"
	if _, err := cold.Resolve(t.Context(), changed); err == nil {
		t.Fatal("changed original profile accepted")
	}
}
func TestReadinessProbeDoesNotBlockHeartbeatAndFailuresDoNotDefault(t *testing.T) {
	f := newSource(t)
	f.reload(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var builds atomic.Int64
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Probe: func(context.Context, agentexec.BuildInput) error {
		close(entered)
		<-release
		return errors.New("native unavailable")
	}, Build: func(agentexec.BuildInput) (harness.Workers, error) { builds.Add(1); return &testkit.Workers{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Refresh(t.Context()) }()
	<-entered
	// This call executes while native setup is deliberately held, with no wall sleep.
	observations, err := p.Observe(t.Context())
	close(release)
	if err != nil || len(observations) != 0 {
		t.Fatal(observations, err)
	}
	if err := <-done; err == nil {
		t.Fatal("failed probe did not request reconciliation retry")
	}
	if o := observed(t, p); o.Available || o.Reason != "adapter_unavailable" || builds.Load() != 0 {
		t.Fatal(o, builds.Load())
	}
}
func TestUnsupportedOrcaBackendIsExplicitWithoutLaunching(t *testing.T) {
	f := newSource(t)
	f.config.Runtimes[0].Kind = agentconfig.OrcaWorker
	f.config.Runtimes[0].CLI = "codex"
	f.reload(t)
	p := newPool(t, f, func(context.Context, agentexec.BuildInput) error { t.Error("unsupported backend launched"); return nil })
	if err := p.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if o := observed(t, p); o.Available || o.Reason != "workspace_backend_not_supported" {
		t.Fatal(o)
	}
}

func TestTransientProbeFailureRequestsExistingReconcilerRetry(t *testing.T) {
	f := newSource(t)
	f.reload(t)
	var probes atomic.Int64
	p := newPool(t, f, func(context.Context, agentexec.BuildInput) error {
		if probes.Add(1) == 1 {
			return errors.New("temporary CLI setup failure")
		}
		return nil
	})
	if err := p.Refresh(t.Context()); err == nil {
		t.Fatal("transient probe failure was reported settled; reconciler will not retry")
	}
	if o := observed(t, p); o.Available || o.Reason != "adapter_unavailable" {
		t.Fatal(o)
	}
	if err := p.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if o := observed(t, p); !o.Available || probes.Load() != 2 {
		t.Fatal(o, probes.Load())
	}
}
