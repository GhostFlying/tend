package agentexec_test

import (
	"context"
	"errors"
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

func TestManagedReadinessStaysBoundToVerifiedExtensionPin(t *testing.T) {
	for _, scenario := range []struct{ name, blocked, failed string }{{"worker_failure", "worker", "worker"}, {"owner_failure", "owner", "owner"}, {"success", "worker", ""}} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newSource(t)
			f.config.FormatVersion = 2
			f.config.Runtimes[0].CLI = "codex"
			f.config.Agents[0].OwnerRuntimeID = "native"
			f.reload(t)
			root := t.TempDir()
			c := &extensions.Consumer{UserHome: t.TempDir(), Config: extensions.Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]extensions.Runtime{"native": {Home: filepath.Join(root, "home")}}}, Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
			a := &extensions.Agent{Consumer: c}
			install := func(text string) string {
				t.Helper()
				r, raw, err := extensions.NewRelease(protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{CategoryID: "implementer", Revision: 1, AgentsMD: text}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := c.Receive(t.Context(), r, nil); err != nil {
					t.Fatal(err)
				}
				if err := a.SetTarget(&protocol.ExtensionTarget{Release: protocol.ExtensionReleaseRef{ID: r.ID, ManifestSHA256: extensions.Hash(raw), ManifestSize: int64(len(raw))}, ManifestPath: "/runner/v1/extensions/" + r.ID + "/manifest", ContentBasePath: "/runner/v1/extensions/" + r.ID + "/content"}); err != nil {
					t.Fatal(err)
				}
				return r.ID
			}
			first := install("original")
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			defer func() { cancel(); <-done }()
			wait := func(id string) {
				t.Helper()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for a.Status().DownloadedRelease != id {
					select {
					case <-t.Context().Done():
						t.Fatal(t.Context().Err())
					case <-ticker.C:
					}
				}
			}
			wait(first)
			entered, release := make(chan struct{}), make(chan struct{})
			block := false
			check := func(ctx context.Context, role string) error {
				if !block {
					return nil
				}
				if role == scenario.blocked {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				if role == scenario.failed {
					return errors.New("new native environment rejected")
				}
				return nil
			}
			p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Extensions: a,
				Probe:      func(ctx context.Context, _ agentexec.BuildInput) error { return check(ctx, "worker") },
				ProbeOwner: func(ctx context.Context, _ agentexec.BuildInput) error { return check(ctx, "owner") },
				Build:      func(agentexec.BuildInput) (harness.Workers, error) { return &testkit.Workers{}, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if err := p.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			next := install("new environment")
			wait(next)
			block = true
			refreshed := make(chan error, 1)
			go func() { refreshed <- p.Refresh(t.Context()) }()
			<-entered
			rows, err := p.Observe(t.Context())
			close(release)
			refreshErr := <-refreshed
			if (refreshErr != nil) != (scenario.failed != "") {
				t.Fatalf("refresh failure = %v, expected role %q", refreshErr, scenario.failed)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Pin.Extensions.ReleaseID == next && row.Available {
					t.Errorf("new unprobed managed pin inherited old readiness: purpose=%q release=%s", row.Pin.Purpose, row.Pin.Extensions.ReleaseID)
				}
			}
			after, err := p.Observe(t.Context())
			if err != nil || len(after) != 2 {
				t.Fatal(after, err)
			}
			for _, row := range after {
				role := "worker"
				if row.Pin.Purpose == "issue_manager" {
					role = "owner"
				}
				if row.Pin.Extensions.ReleaseID != next || row.Available != (role != scenario.failed) {
					t.Fatalf("readiness was not published for exact new pin: %+v", row)
				}
			}
			for _, row := range after {
				if row.Pin.Purpose == "" {
					p.Reject(row.Pin, "runtime_policy_rejected")
				}
			}
			rejected, err := p.Observe(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rejected {
				if row.Pin.Purpose == "" && row.Available {
					t.Fatal("cached managed readiness hid explicit refusal", row)
				}
			}

		})
	}
}
