package serverkit

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestHostedExtensionFailureDoesNotBlockUnrelatedConversation(t *testing.T) {
	root := t.TempDir()
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 845)
	ledger, err := store.Open(t.Context(), filepath.Join(root, "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	definitions := agentconfig.Config{FormatVersion: 2, Runtimes: []agentconfig.RuntimeBinding{{ID: "native", Revision: 1, Kind: agentconfig.HostedManager, CLI: "codex", ExecutableRef: "fixture", DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.Consultation}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "planner", Revision: 1, DisplayName: "Planner", RuntimeID: "native", Categories: []string{"planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.Consultation}, Model: "fixture", Enabled: true}}}
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "planner", Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(ledger, ids, clock.Now, app.WithHostedConfiguration(prepared), app.WithHostedExtensions([]string{"native"}))
	exec := func(p protocol.Principal, c app.Command, target protocol.Target, payload any, revision *int64) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		op, err := a.Execute(t.Context(), p, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: c.Operation(), Target: target, Payload: raw, ExpectedControlRevision: revision}, c)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal("fixture command failed", c.Operation(), op, err)
		}
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "fixture"}
	zero := int64(0)
	exec(owner, app.PutAgentCategory{}, protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, &zero)
	exec(protocol.Principal{Kind: protocol.PrincipalSystem}, app.AcceptHostedConfiguration{Prepared: prepared}, protocol.Target{Type: "hosted_configuration"}, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()}, &zero)
	release, raw, err := extensions.NewRelease(protocol.ExtensionCatalog{Categories: []protocol.ExtensionBinding{{CategoryID: "planner", AgentsMD: "managed"}}})
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.ExtensionReleaseRef{ID: release.ID, ManifestSHA256: extensions.Hash(raw), ManifestSize: int64(len(raw))}
	if err := ledger.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertExtensionRelease(t.Context(), release, ref, "fixture"); err != nil {
			return err
		}
		return tx.InsertExtensionState(t.Context(), protocol.ExtensionState{Revision: 1, Catalog: release.Catalog, Published: &ref}, "fixture")
	}); err != nil {
		t.Fatal(err)
	}
	exec(owner, app.CreateConsultation{}, protocol.Target{Type: "conversation"}, protocol.CreateConsultationPayload{AgentRef: "hosted/planner", Title: "Healthy consultation"}, nil)
	attempts := 0
	consumer := &extensions.Consumer{UserHome: t.TempDir(), Config: extensions.Configuration{Directory: filepath.Join(root, "cache"), Runtimes: map[string]extensions.Runtime{"native": {Home: filepath.Join(root, "home")}}}, Validate: func(context.Context, codexenv.Profile, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("first combination unavailable")
		}
		return nil
	}}
	if err := prepareHostedExtensions(t.Context(), a, MainAgentConfig{Instance: "server", extensionConsumer: consumer}); err == nil {
		t.Fatal("first preparation failure was hidden")
	}
	if attempts != 2 {
		t.Fatal("first failure prevented the healthy consultation from preparing", attempts)
	}
	status, err := consumer.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ready := 0
	for _, view := range status.Prepared {
		if view.Ready {
			ready++
		}
	}
	if ready != 1 {
		t.Fatal("healthy conversation has no ready snapshot", status)
	}
}
