package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestAgentCatalogOwnerHTTPAndCLIReadSamePublicFacts(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	zero := int64(0)
	status, result := e.op(token, e.envelope("categories.put", protocol.Target{Type: "category", ID: "worker"}, &zero, protocol.PutAgentCategoryPayload{Title: "Worker", State: "active"}))
	if status != http.StatusOK || result.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, result)
	}
	cfg := agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.AppServerWorker, ExecutableRef: "private-cli", Enabled: true, DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}}}, Agents: []agentconfig.AgentDefinition{{LocalID: "worker", Revision: 1, DisplayName: "Worker", RuntimeID: "runtime", Categories: []string{"worker"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "worker-model", Enabled: true}}}
	definition, err := agentconfig.Validate("host-a", cfg, agentconfig.SourceReference{Kind: agentconfig.Inline, Reference: "private-config"})
	if err != nil {
		t.Fatal(err)
	}
	_, hash, _ := definition.CanonicalPublic()
	runtime := &protocol.RunnerRuntime{InstanceID: "catalog-process", Generation: 1, Sequence: 1, Protocol: 1, ProtocolMin: 0, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityAgentInventory, protocol.CapabilityRunnerHeartbeat}}
	exec := func(operation string, revision *int64, payload any, command app.Command) {
		t.Helper()
		env := e.envelope(operation, protocol.Target{Type: "host", ID: "host-a"}, revision, payload)
		op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "host-a"}, &env, command)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
	}
	exec(protocol.OpRunnerHello, nil, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: runtime}, app.Hello{})
	exec(protocol.OpRunnerInventoryRegister, &zero, protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: 1, SnapshotID: "catalog-snapshot", PublicSHA256: hash, Projection: definition.Public()}, app.RegisterAgentInventory{})
	for _, path := range []string{"/api/v1/categories", "/api/v1/hosts/host-a/agents"} {
		for _, auth := range []string{"", e.retiredAgentToken(clientID)} {
			code := e.do("GET", path, auth, nil, nil)
			if code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Fatal("catalog accepted non-owner", path, code)
			}
		}
	}
	if code := e.do("GET", "/api/v1/hosts/missing/agents", token, nil, nil); code != http.StatusNotFound {
		t.Fatal(code)
	}
	if code := e.do("GET", "/api/v1/categories?limit=oops", token, nil, nil); code != http.StatusBadRequest {
		t.Fatal(code)
	}
	var catalog protocol.RunnerAgentCatalog
	if code := e.do("GET", "/api/v1/hosts/host-a/agents", token, nil, &catalog); code != http.StatusOK || catalog.Current == nil || catalog.Current.State != "registered_staged" || !catalog.Online || len(catalog.Executors) != 0 {
		t.Fatal(code, catalog)
	}
	var page protocol.AgentCategoryPage
	if code := e.do("GET", "/api/v1/categories?limit=1", token, nil, &page); code != http.StatusOK || len(page.Items) != 1 || page.Items[0].CategoryID != "worker" {
		t.Fatal(code, page)
	}
	count := func() [3]int {
		t.Helper()
		var result [3]int
		if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
			for i, table := range []string{"request_records", "events", "outbox"} {
				if err := tx.Q().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&result[i]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := count()
	dir := t.TempDir()
	deployment, _ := e.app.DeploymentID(t.Context())
	for name, data := range map[string]any{"config.json": map[string]string{"server": e.url}, "credentials.json": map[string]string{"client_id": "catalog-cli", "owner_token": token, "deployment_id": deployment}} {
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	cliEnv := &cli.Env{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, ConfigDir: dir, HTTP: http.DefaultClient, IDs: e.ids}
	if code := cli.Main(t.Context(), cliEnv, []string{"agents", "show", "--host", "host-a"}); code != 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}
	var fromCLI protocol.RunnerAgentCatalog
	if json.Unmarshal(stdout.Bytes(), &fromCLI) != nil || !reflect.DeepEqual(catalog, fromCLI) {
		t.Fatal(fromCLI, catalog)
	}
	stdout.Reset()
	if code := cli.Main(t.Context(), cliEnv, []string{"category", "list", "--limit", "1"}); code != 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}
	var cliPage protocol.AgentCategoryPage
	if json.Unmarshal(stdout.Bytes(), &cliPage) != nil || !reflect.DeepEqual(page, cliPage) {
		t.Fatal(cliPage, page)
	}
	if before != count() {
		t.Fatal("CLI read wrote business state")
	}
}
