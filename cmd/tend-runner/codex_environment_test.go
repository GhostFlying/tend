//go:build linux

package main

import (
	"context"
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
		if err := resolveRolloutRoots(&cfg); err != nil {
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
