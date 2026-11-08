//go:build linux

package serverkit

import (
	"context"
	"github.com/GhostFlying/tend/internal/extensions"
	"os"
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

func TestHostedManagedRecoveryDoesNotReopenMissingBaseConfig(t *testing.T) {
	root := t.TempDir()
	ledger, err := store.Open(t.Context(), filepath.Join(root, "ledger.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	a := app.New(ledger, protocol.NewULIDGen(time.Now, nil), time.Now)
	base := filepath.Join(root, "base.toml")
	if err := os.WriteFile(base, nil, 0600); err != nil {
		t.Fatal(err)
	}
	release, raw, err := extensions.NewRelease(protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{CategoryID: "planner", AgentsMD: "retained"}}})
	if err != nil {
		t.Fatal(err)
	}
	reference := protocol.ExtensionReleaseRef{ID: release.ID, ManifestSHA256: extensions.Hash(raw), ManifestSize: int64(len(raw))}
	if err := ledger.Write(t.Context(), func(tx *store.Tx) error { return tx.InsertExtensionRelease(t.Context(), release, reference, "fixture") }); err != nil {
		t.Fatal(err)
	}
	selected := hostedconfig.Selection{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "hosted", LocalID: "planner"}}
	selected.Runtime.ID = "native"
	combination, err := extensions.Resolve(release, "planner", selected.Identity)
	if err != nil {
		t.Fatal(err)
	}
	selected.Extensions = combination.Pin
	consumer := &extensions.Consumer{UserHome: t.TempDir(), Config: extensions.Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]extensions.Runtime{"native": {Home: filepath.Join(root, "home"), BaseConfig: base}}}, Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	cfg := MainAgentConfig{Instance: "coordinator", extensionConsumer: consumer}
	binding, execution, err := hostedCodexEnvironment(t.Context(), a, cfg, selected, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Pin(t.Context(), execution); err != nil {
		t.Fatal(err)
	}
	if err := a.SetMainThread(t.Context(), selected.BindingInstance(cfg.Instance), "original-thread"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	restored, restoredExecution, err := hostedCodexEnvironment(t.Context(), a, cfg, selected, root)
	if err != nil || restored == nil || *restored != *binding || execution != restoredExecution {
		t.Fatal("hosted receipt recovery required new-work config", restored, err)
	}
}
