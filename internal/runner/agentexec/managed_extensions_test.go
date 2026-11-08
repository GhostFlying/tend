package agentexec_test

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/agentconfig"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestManagedExecutorsGateEachCategoryAndKeepOriginalRecoveryPin(t *testing.T) {
	f := newSource(t)
	f.config.FormatVersion = 2
	f.config.Runtimes[0].CLI = "codex"
	f.config.Agents[0].OwnerRuntimeID = "native"
	f.config.Agents[0].Categories = []string{"implementer", "reviewer"}
	f.reload(t)
	root := t.TempDir()
	consumer := &extensions.Consumer{UserHome: t.TempDir(), Config: extensions.Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]extensions.Runtime{"native": {Home: filepath.Join(root, "home")}}}, Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	a := &extensions.Agent{Consumer: consumer}
	install := func(text string) string {
		t.Helper()
		r, raw, err := extensions.NewRelease(protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{CategoryID: "implementer", Revision: 1, AgentsMD: text}, {CategoryID: "reviewer", Revision: 1, AgentsMD: "review instructions"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = consumer.Receive(t.Context(), r, func(context.Context, protocol.ExtensionResource) (io.ReadCloser, error) {
			return nil, errors.New("no content is expected")
		}); err != nil {
			t.Fatal(err)
		}
		if err = a.SetTarget(&protocol.ExtensionTarget{Release: protocol.ExtensionReleaseRef{ID: r.ID, ManifestSHA256: extensions.Hash(raw), ManifestSize: int64(len(raw))}, ManifestPath: "/runner/v1/extensions/" + r.ID + "/manifest", ContentBasePath: "/runner/v1/extensions/" + r.ID + "/content"}); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	first := install("first implementation instructions")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitDownloaded := func(id string) {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for a.Status().DownloadedRelease != id {
			select {
			case <-deadline.C:
				t.Fatal("cached target was not acknowledged")
			case <-ticker.C:
			}
		}
	}
	waitDownloaded(first)
	probe := func(_ context.Context, input agentexec.BuildInput) error {
		if input.Environment == nil {
			return errors.New("managed factory was probed without its view")
		}
		return nil
	}
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Extensions: a, Probe: probe, ProbeOwner: func(_ context.Context, input agentexec.BuildInput) error {
		if input.Environment.Extensions.CategoryID == "reviewer" {
			return errors.New("owner isolation rejected only this category")
		}
		return nil
	}, Build: func(input agentexec.BuildInput) (harness.Workers, error) {
		if input.Environment == nil {
			return nil, errors.New("managed factory has no view")
		}
		return &testkit.Workers{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err = p.Refresh(t.Context()); err == nil {
		t.Fatal("failed owner category probe was hidden")
	}
	rows, err := p.Observe(t.Context())
	if err != nil || len(rows) != 4 {
		t.Fatal(rows, err)
	}
	var original protocol.RunExecutionPin
	for _, row := range rows {
		if row.Pin.Extensions.ReleaseID != first {
			t.Fatal("executor lacks exact published release", row)
		}
		if row.Pin.Purpose == "issue_manager" && row.Pin.Extensions.CategoryID == "reviewer" {
			if row.Available {
				t.Fatal("another category granted owner readiness", row)
			}
		} else if !row.Available {
			t.Fatal("unrelated ready execution was blocked", row)
		}
		if row.Pin.Purpose == "" && row.Pin.Extensions.CategoryID == "implementer" {
			original = row.Pin
		}
	}
	if _, err = p.Resolve(t.Context(), original); err != nil {
		t.Fatal("initial managed execution unavailable", err)
	}
	next := install("updated implementation instructions")
	waitDownloaded(next)
	_ = p.Refresh(t.Context())
	rows, err = p.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Pin.Extensions.ReleaseID != next {
			t.Fatal("new executor still targets old release")
		}
	}
	if _, err = p.Resolve(t.Context(), original); err != nil {
		t.Fatal("publication invalidated original recovery pin", err)
	}
	if original.Extensions.ReleaseID != first {
		t.Fatal("original value pin mutated")
	}
}

func TestManagedOwnerReadinessDoesNotDependOnDisabledWorker(t *testing.T) {
	f := newSource(t, "native", "owner")
	f.config.FormatVersion = 2
	f.config.Runtimes[0].CLI = "codex"
	f.config.Runtimes[0].Enabled = false
	owner := f.config.Runtimes[0]
	owner.ID = "owner"
	owner.Enabled = true
	owner.DeclaredPurposes = []agentconfig.Purpose{agentconfig.IssueManager}
	f.config.Runtimes = append(f.config.Runtimes, owner)
	f.config.Agents[0].OwnerRuntimeID = "owner"
	f.reload(t)
	root := t.TempDir()
	c := &extensions.Consumer{UserHome: t.TempDir(), Config: extensions.Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]extensions.Runtime{"owner": {Home: filepath.Join(root, "owner")}}}, Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	release, raw, err := extensions.NewRelease(protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{CategoryID: "implementer"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Receive(t.Context(), release, nil); err != nil {
		t.Fatal(err)
	}
	a := &extensions.Agent{Consumer: c}
	if err := a.SetTarget(&protocol.ExtensionTarget{Release: protocol.ExtensionReleaseRef{ID: release.ID, ManifestSHA256: extensions.Hash(raw), ManifestSize: int64(len(raw))}, ManifestPath: "/runner/v1/extensions/" + release.ID + "/manifest", ContentBasePath: "/runner/v1/extensions/" + release.ID + "/content"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for a.Status().DownloadedRelease != release.ID {
		select {
		case <-deadline.C:
			t.Fatal("cached release not ready")
		case <-time.After(time.Millisecond):
		}
	}
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Extensions: a, Probe: func(context.Context, agentexec.BuildInput) error { t.Error("disabled worker was probed"); return nil }, ProbeOwner: func(context.Context, agentexec.BuildInput) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := p.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.Pin.Purpose == "issue_manager" {
			found = true
			if !row.Available {
				t.Fatal("healthy owner inherited disabled-worker readiness", row)
			}
		} else if row.Available {
			t.Fatal("disabled worker accepted new work")
		}
	}
	if !found {
		t.Fatal("owner observation missing")
	}
}

func TestManagedRecoveryRestoresReceiptsAfterLocalCredentialDisappears(t *testing.T) {
	t.Setenv("TEND_TEST_AGENTEXEC_MCP_TOKEN", "fixture")
	f := newSource(t)
	f.config.FormatVersion = 2
	f.config.Runtimes[0].CLI = "codex"
	f.config.Agents[0].OwnerRuntimeID = "native"
	f.reload(t)
	root := t.TempDir()
	c := &extensions.Consumer{UserHome: t.TempDir(), Config: extensions.Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]extensions.Runtime{"native": {Home: filepath.Join(root, "home")}}}, Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	resource := protocol.ExtensionResource{Kind: "mcp", ID: "required", Version: "1", MCP: &protocol.ExtensionMCP{Transport: "http", URL: "https://example.test/mcp", BearerTokenEnvVar: "TEND_TEST_AGENTEXEC_MCP_TOKEN", Required: true}}
	release, _, err := extensions.NewRelease(protocol.ExtensionCatalog{Resources: []protocol.ExtensionResource{resource}, Categories: []protocol.ExtensionBinding{{CategoryID: "implementer", Choices: []protocol.ExtensionChoice{{Kind: "mcp", ID: "required", Version: "1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Receive(t.Context(), release, nil); err != nil {
		t.Fatal(err)
	}
	combo, err := extensions.Resolve(release, "implementer", protocol.AgentIdentity{ExecutionEnvironmentID: "runner-a", LocalID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Prepare(t.Context(), combo.Pin, "native"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEND_TEST_AGENTEXEC_MCP_TOKEN", "")
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Extensions: &extensions.Agent{Consumer: c}, Probe: func(context.Context, agentexec.BuildInput) error {
		t.Error("recovery probed new-work readiness")
		return errors.New("not ready")
	}, ProbeOwner: func(context.Context, agentexec.BuildInput) error {
		t.Error("receipt recovery probed a business turn")
		return errors.New("not ready")
	}, Build: func(input agentexec.BuildInput) (harness.Workers, error) {
		if input.Environment == nil {
			return nil, errors.New("original factory lost managed view")
		}
		return &testkit.Workers{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	rows, err := p.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatal("worker/owner inventory missing", rows)
	}
	retained := []protocol.RunExecutionPin{}
	for _, row := range rows {
		pin := row.Pin
		pin.Extensions = combo.Pin
		retained = append(retained, pin)
		if pin.Purpose == "issue_manager" {
			resolved, err := p.ResolveManagerReceipt(t.Context(), pin)
			if err != nil || resolved.OpenRun == nil {
				t.Fatal("original owner receipt rejected", err)
			}
		} else {
			if _, err := p.Resolve(t.Context(), pin); err != nil {
				t.Fatal("original worker recovery rejected", err)
			}
		}
	}
	p.SetRetained(retained)
	f.config.Agents[0].Revision++
	f.config.Agents[0].Model = "new-current-model"
	f.reload(t)
	_ = p.Refresh(t.Context())
	restored, err := p.ObserveRetained(t.Context())
	if err != nil || len(restored) != 2 {
		t.Fatal("retained readiness missing after snapshot replacement", restored, err)
	}
	for _, row := range restored {
		if !row.Available {
			t.Fatal("resolved managed factory was marked unavailable", row)
		}
	}

}
