//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/codexenv"
)

func TestMappedRolloutRootsPreserveExplicitSettings(t *testing.T) {
	root := t.TempDir()
	service := &codexenv.Service{Root: filepath.Join(root, "environments"), Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	profile := codexenv.Profile{ID: "worker", Home: filepath.Join(root, "home"), UserHome: t.TempDir(), Enabled: true}
	if _, err := service.Execute(t.Context(), codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	bindings := &codexenv.Bindings{Directory: service.Root, Runtimes: map[string]string{"native": profile.ID}}
	for _, input := range [][]string{nil, {}, {filepath.Join(root, "explicit")}} {
		cfg := config{CodexEnvironments: bindings, RolloutRoots: input}
		if err := resolveRolloutRoots(&cfg, root); err != nil {
			t.Fatal(err)
		}
		if input == nil {
			if !slices.Contains(cfg.RolloutRoots, filepath.Join(profile.Home, "sessions")) || len(cfg.RolloutRoots) != 3 {
				t.Fatal(cfg.RolloutRoots)
			}
		} else if !slices.Equal(cfg.RolloutRoots, input) {
			t.Fatal("explicit roots changed", input, cfg.RolloutRoots)
		}
	}
}

func TestDefaultRootsRetainUnmappedExecutionHomes(t *testing.T) {
	root := t.TempDir()
	service := &codexenv.Service{Root: filepath.Join(root, "environments"), Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	profile := codexenv.Profile{ID: "original", Home: filepath.Join(root, "original-home"), UserHome: t.TempDir(), Enabled: true}
	if _, err := service.Execute(t.Context(), codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "codex-rollout-roots")
	if err := codexenv.RecordSessionHome(t.Context(), registry, &codexenv.Binding{Directory: service.Root, ID: profile.ID}); err != nil {
		t.Fatal(err)
	}
	cfg := config{}
	if err := resolveRolloutRoots(&cfg, root); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.RolloutRoots, filepath.Join(profile.Home, "sessions")) {
		t.Fatal("unmapping removed original transcript root", cfg.RolloutRoots)
	}
	if err := os.WriteFile(filepath.Join(registry, "homes.json"), []byte("invalid registry"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range [][]string{{}, {filepath.Join(root, "explicit")}} {
		cfg = config{RolloutRoots: explicit}
		if err := resolveRolloutRoots(&cfg, root); err != nil || !slices.Equal(cfg.RolloutRoots, explicit) {
			t.Fatal("explicit roots did not retain authority", cfg.RolloutRoots, err)
		}
	}
}

func TestUnavailableMappingDoesNotBlockRetainedRunnerRoots(t *testing.T) {
	root := t.TempDir()
	service := &codexenv.Service{Root: filepath.Join(root, "environments"), Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	profile := codexenv.Profile{ID: "original", Home: filepath.Join(root, "original-home"), UserHome: t.TempDir(), Enabled: true}
	if _, err := service.Execute(t.Context(), codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	if err := codexenv.RecordSessionHome(t.Context(), filepath.Join(root, "codex-rollout-roots"), &codexenv.Binding{Directory: service.Root, ID: profile.ID}); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{service.Root, filepath.Join(root, "missing-store")} {
		cfg := config{CodexEnvironments: &codexenv.Bindings{Directory: directory, Runtimes: map[string]string{"native": "missing-environment", "unrelated": profile.ID}}}
		if err := resolveRolloutRoots(&cfg, root); err != nil {
			t.Fatal("unavailable new mapping blocked original recovery", err)
		}
		if !slices.Contains(cfg.RolloutRoots, filepath.Join(profile.Home, "sessions")) || len(cfg.RolloutRoots) < 3 {
			t.Fatal("retained/default transcript paths lost", cfg.RolloutRoots)
		}
	}
}
