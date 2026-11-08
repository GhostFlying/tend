//go:build linux

package serverkit

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedCodexEnvironmentPreservesOriginalBinding(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(t.Context(), filepath.Join(root, "ledger.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := app.New(s, protocol.NewULIDGen(time.Now, nil), time.Now)
	cfg := MainAgentConfig{Instance: "coordinator", CodexEnvironments: &codexenv.Bindings{Directory: filepath.Join(root, "original-store"), Runtimes: map[string]string{"native": "original"}}}
	selected := hostedconfig.Selection{}
	selected.Runtime.ID = "native"
	original, execution, err := hostedCodexEnvironment(t.Context(), a, cfg, selected, root)
	if err != nil || original == nil {
		t.Fatal(original, err)
	}
	if err = a.SetMainThread(t.Context(), selected.BindingInstance(cfg.Instance), "original-native-thread"); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []*codexenv.Bindings{{Directory: filepath.Join(root, "replacement-store"), Runtimes: map[string]string{"native": "replacement"}}, nil} {
		cfg.CodexEnvironments = changed
		restored, restoredExecution, err := hostedCodexEnvironment(t.Context(), a, cfg, selected, root)
		if err != nil || restored == nil || *restored != *original || restoredExecution != execution {
			t.Fatal("mapping redirected coordinator", restored, restoredExecution, err)
		}
	}
	cfg.Instance = "legacy"
	cfg.CodexEnvironments = &codexenv.Bindings{Directory: filepath.Join(root, "new-store"), Runtimes: map[string]string{"native": "new"}}
	if err = a.SetMainThread(t.Context(), selected.BindingInstance(cfg.Instance), "legacy-native-thread"); err != nil {
		t.Fatal(err)
	}
	legacy, _, err := hostedCodexEnvironment(t.Context(), a, cfg, selected, root)
	if err != nil || legacy != nil {
		t.Fatal("legacy coordinator adopted a new environment", legacy, err)
	}
	cfg.Instance = "fresh-coordinator"
	fresh, freshExecution, err := hostedCodexEnvironment(t.Context(), a, cfg, selected, root)
	if err != nil || fresh == nil || fresh.ID != "new" || freshExecution == execution {
		t.Fatal("new coordinator did not use selected environment", fresh, freshExecution, err)
	}
}
