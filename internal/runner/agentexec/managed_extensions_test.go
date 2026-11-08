package agentexec_test

import (
	"context"
	"errors"
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
