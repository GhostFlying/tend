package api

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedAgentInventoryRead(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		e := newEnv(t)
		token := e.login(protocol.ScopeOwner)
		var got protocol.HostedAgentInventory
		if status := e.do(http.MethodGet, "/api/v1/hosted-agents", token, nil, &got); status != http.StatusOK {
			t.Fatalf("empty optional configuration: status %d, inventory %+v", status, got)
		}
		want := protocol.HostedAgentInventory{State: "unconfigured", Projection: protocol.AgentInventoryProjection{FormatVersion: 1, SourceKind: protocol.AgentSourceInline, Runtimes: []protocol.AgentInventoryRuntime{}, Agents: []protocol.AgentInventoryDefinition{}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("inventory = %+v, want %+v", got, want)
		}
		if n := e.count(`SELECT COUNT(*) FROM hosted_configuration_versions`); n != 0 {
			t.Fatalf("read created hosted configuration: %d", n)
		}
	})
	t.Run("configured", func(t *testing.T) {
		e := newEnv(t)
		zero := int64(0)
		put := e.envelope("categories.put", protocol.Target{Type: "category", ID: "planner"}, &zero, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"})
		if op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, &put, app.PutAgentCategory{}); err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
		prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "manager", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "HOSTED_EXECUTABLE", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "manager", Revision: 1, DisplayName: "Manager", RuntimeID: "runtime", Categories: []string{"planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "model", Enabled: true}}}})
		if err != nil {
			t.Fatal(err)
		}
		admit := e.envelope("hosted.configure", protocol.Target{Type: "hosted_configuration"}, &zero, map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()})
		if op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, &admit, app.AcceptHostedConfiguration{Prepared: prepared}); err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
		want, err := e.app.HostedAgentInventory(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var got protocol.HostedAgentInventory
		if status := e.do(http.MethodGet, "/api/v1/hosted-agents", e.login(protocol.ScopeOwner), nil, &got); status != http.StatusOK {
			t.Fatal(status)
		}
		if !reflect.DeepEqual(got, want) || got.State != "registered_staged" || got.ConfigurationRevision != 1 || got.PublicSHA256 == "" || got.AcceptedRequestID != admit.RequestID || len(got.Projection.Agents) != 1 {
			t.Fatalf("configured inventory changed: got %+v, want %+v", got, want)
		}
	})
	t.Run("authentication required", func(t *testing.T) {
		e := newEnv(t)
		if status := e.do(http.MethodGet, "/api/v1/hosted-agents", "", nil, nil); status != http.StatusUnauthorized {
			t.Fatal(status)
		}
	})
	t.Run("storage failure remains an error", func(t *testing.T) {
		e := newEnv(t)
		token := e.login(protocol.ScopeOwner)
		if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `DROP TABLE hosted_configuration_versions`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if status := e.do(http.MethodGet, "/api/v1/hosted-agents", token, nil, &body); status != http.StatusInternalServerError || body.Error.Code != "internal" {
			t.Fatalf("storage error hidden: status %d, response %+v", status, body)
		}
	})
}
