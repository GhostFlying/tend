package archiveseed_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/archiveseed"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestStaticSeedCreatesEmptyLedgerAndRejectsOldOwnerAndRunnerIdentity(t *testing.T) {
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 881)
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "tend.db")
	oldStore, err := store.Open(t.Context(), filepath.Join(sourceRoot, "auth-fixture.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	oldApp := app.New(oldStore, ids, clock.Now)
	oldToken := strings.Repeat("a", 64)
	digest := sha256.Sum256([]byte(oldToken))
	oldSecret := bytes.Repeat([]byte{7}, 32)
	cfg := app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "example", RemoteURL: "https://example.com/repo.git"}}, Hosts: []app.HostConfig{{HostID: "runner-a", OS: "linux", Arch: "amd64", Slots: 2, Repos: []string{"example"}, TokenHash: hex.EncodeToString(digest[:]), Nodes: []string{"old-node"}, Worker: protocol.WorkerAppServer}}}
	execute := func(operation string, target protocol.Target, value any, command app.Command) {
		t.Helper()
		body, _ := json.Marshal(value)
		principal := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "fixture-client"}
		if operation == "config.sync" {
			principal.Kind = protocol.PrincipalSystem
		}
		if operation == protocol.OpRunnerHello {
			principal = protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "runner-a"}
		}
		result, err := oldApp.Execute(t.Context(), principal, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: body}, command)
		if err != nil || result.Error != nil {
			t.Fatal(result, err)
		}
	}
	execute("config.sync", protocol.Target{Type: "config"}, cfg, app.SyncConfig{})
	execute(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "runner-a"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: &protocol.RunnerRuntime{InstanceID: "old", Generation: 1, Sequence: 1, Protocol: protocol.PreviousRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.PreviousRunnerProtocol, Mode: protocol.RunnerNormal}}, app.Hello{})
	execute("tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": "old pending intent", "spec": domain.Spec{Goal: "Historical task never restored", Acceptance: []string{"Evidence"}, Deliverables: []string{domain.DeliveryReport}, Workspace: domain.Workspace{Mode: domain.WorkspaceTemporary}}}, app.CreateTask{})
	if err = oldStore.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertAgentCategory(t.Context(), protocol.AgentCategory{CategoryID: "implementer", Revision: 4, Title: "Implementation", Description: "Static role category", State: "active"}, "old-category-request"); err != nil {
			return err
		}
		return tx.PutConversationSettings(t.Context(), store.ConversationSettings{Model: "ordinary-model", Effort: "high", Revision: 3}, clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	oldID, err := oldApp.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	identity := netx.Static{Identity: &netx.Identity{User: "owner", Node: "new-node"}}
	oldAPI := api.New(oldApp, oldStore, identity, oldSecret, api.Config{Owners: []string{"owner"}, Now: clock.Now}, logger).Handler()
	session := func(handler http.Handler) protocol.SessionView {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"client_id":"fixture-client","scope":"owner"}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var view protocol.SessionView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	oldSession := session(oldAPI)
	if err = oldStore.Close(); err != nil {
		t.Fatal(err)
	}
	historical, err := testkit.ArchiveLedger57(t.Context(), sourcePath, "../store/migrations")
	if err != nil {
		t.Fatal(err)
	}
	seeds := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO deployment_identity VALUES(1,?)`, []any{oldID}},
		{`INSERT INTO repos(repo,remote_url,base_candidates_json,created_at,updated_at) VALUES('example','https://example.com/repo.git','[]',1,1)`, nil},
		{`UPDATE projects SET repos_json='["example"]' WHERE project='default'`, nil},
		{`INSERT INTO hosts(host_id,os,arch,labels_json,repos_json,slots,token_hash,nodes_json,status,created_at,updated_at,worker) VALUES('runner-a','linux','amd64','[]','["example"]',2,?,'["old-node"]','active',1,1,'appserver')`, []any{hex.EncodeToString(digest[:])}},
		{`INSERT INTO agent_category_versions VALUES('implementer',4,'Implementation','Static role category','active','old-category-request')`, nil},
		{`INSERT INTO conversation_settings(singleton,model,effort,revision,updated_at) VALUES(1,'ordinary-model','high',3,1)`, nil},
		{`INSERT INTO tasks(task_id,title,lifecycle,control_mode,control_revision,spec_version,priority,block_reason,created_at,updated_at) VALUES('historical-task','old pending intent','queued','managed',1,1,0,'none',1,1)`, nil},
		{`INSERT INTO task_specs VALUES('historical-task',1,'{"goal":"Historical task never restored"}',NULL,1)`, nil},
	}
	for _, seed := range seeds {
		if _, err = historical.ExecContext(t.Context(), seed.query, seed.args...); err != nil {
			t.Fatal(seed.query, err)
		}
	}
	if err = historical.Close(); err != nil {
		t.Fatal(err)
	}
	archiveRoot := filepath.Join(t.TempDir(), "archive")
	if _, err = archive.Capture(t.Context(), archive.CaptureOptions{Database: sourcePath, Destination: archiveRoot, WritersStopped: true, CapturedAt: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	reader, err := archive.Open(t.Context(), archiveRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	seed, err := reader.ExportSeed(t.Context(), archive.Selection{Repositories: []string{"example"}, Projects: []string{"default"}, Hosts: []string{"runner-a"}, Categories: []string{"implementer"}, MainSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(seed)
	for _, forbidden := range []string{oldToken, hex.EncodeToString(digest[:]), "old-node", "token_hash", "runtime_json", "old pending intent"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatal("static seed included old activity/credentials", forbidden)
		}
	}
	target := filepath.Join(t.TempDir(), "new-ledger")
	options := archiveseed.Options{Target: target, ArchiveRoot: archiveRoot, Seed: seed, Bindings: archiveseed.Bindings{Hosts: []archiveseed.HostBinding{{HostID: "runner-a", Nodes: []string{"new-node"}}}}, Now: clock.Now, IDs: testkit.NewIDGen(clock, 882), Random: bytes.NewReader(bytes.Repeat([]byte{9}, 96))}
	receipt, err := archiveseed.Initialize(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.DeploymentID == oldID || receipt.State != "seeded_offline" {
		t.Fatal(receipt)
	}
	if _, err = archiveseed.Initialize(t.Context(), options); err == nil {
		t.Fatal("existing target accepted")
	}
	var secrets archiveseed.Secrets
	if err = archive.ReadJSON(filepath.Join(target, "bootstrap-secrets.json"), &secrets); err != nil {
		t.Fatal(err)
	}
	newSecret, err := hex.DecodeString(secrets.ServerSecret)
	if err != nil || bytes.Equal(newSecret, oldSecret) || secrets.RunnerTokens["runner-a"] == oldToken {
		t.Fatal("fresh credentials missing", err)
	}
	s, err := store.Open(t.Context(), filepath.Join(target, "tend.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, table := range []string{"tasks", "task_specs", "plan_actions", "runs", "artifacts", "acceptances", "decisions", "approvals", "work_items", "issue_controls", "leases", "outbox", "inbox", "conversation_messages", "main_turns", "agent_inventory_versions", "host_runtime"} {
			var count int
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("old business/observations restored", table, count)
			}
		}
		category, err := store.GetAgentCategory(t.Context(), tx.Q(), "implementer")
		if err != nil || category.Revision != 1 || category.Title != "Implementation" {
			t.Fatal("category not freshly initialized", category, err)
		}
		host, err := store.GetHost(t.Context(), tx.Q(), "runner-a")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(host.Nodes, []string{"new-node"}) || host.TokenHash == hex.EncodeToString(digest[:]) {
			t.Fatal("host retained old authentication")
		}
		settings, err := store.GetConversationSettings(t.Context(), tx.Q())
		if err != nil {
			return err
		}
		if settings.Model != "ordinary-model" || settings.Effort != "high" || settings.Revision != 1 {
			t.Fatal(settings)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newApp := app.New(s, testkit.NewIDGen(clock, 883), clock.Now)
	newAPI := api.New(newApp, s, identity, newSecret, api.Config{Owners: []string{"owner"}, Now: clock.Now}, logger).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ops", strings.NewReader(`{}`))
	request.Header.Set("X-Tend-Deployment-ID", oldID)
	request.Header.Set("Authorization", "Bearer "+oldSession.Token)
	response := httptest.NewRecorder()
	newAPI.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatal("old deployment mutation was not fenced", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	request.Header.Set("Authorization", "Bearer "+oldSession.Token)
	response = httptest.NewRecorder()
	newAPI.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("old owner signing identity accepted", response.Code)
	}
	if current := session(newAPI); current.DeploymentID != receipt.DeploymentID || current.Token == oldSession.Token {
		t.Fatal("new owner session failed")
	}
	runnerIdentity := netx.Static{Identity: &netx.Identity{Node: "new-node"}}
	runners := runnerapi.New(newApp, runnerIdentity, newSecret, runnerapi.Config{}, logger).Handler()
	rpc := func(token, run string) int {
		t.Helper()
		payload, err := json.Marshal(protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: &protocol.RunnerRuntime{InstanceID: "fresh-runner", Generation: 1, Sequence: 1, Protocol: protocol.PreviousRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.PreviousRunnerProtocol, Mode: protocol.RunnerNormal}})
		if err != nil {
			t.Fatal(err)
		}
		env := protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: protocol.OpRunnerHello, Target: protocol.Target{Type: "host", ID: "runner-a"}, Payload: payload}
		body, _ := json.Marshal(env)
		q := httptest.NewRequest(http.MethodPost, runnerapi.Path, bytes.NewReader(body))
		q.Header.Set("Authorization", "Bearer "+token)
		if run == "" {
			q.Header.Set(runnerapi.HeaderHost, "runner-a")
		} else {
			q.Header.Set(runnerapi.HeaderRun, run)
		}
		answer := httptest.NewRecorder()
		runners.ServeHTTP(answer, q)
		return answer.Code
	}
	if code := rpc(oldToken, ""); code != http.StatusForbidden {
		t.Fatal("old runner token accepted", code)
	}
	if code := rpc(runnerapi.RunToken(oldSecret, "old-lease"), "old-run"); code != http.StatusForbidden {
		t.Fatal("old run receipt credential accepted", code)
	}
	if code := rpc(secrets.RunnerTokens["runner-a"], ""); code != http.StatusOK {
		t.Fatal("fresh runner registration failed", code)
	}
	// Seed did not create/adopt a runner journal or network state.
	for _, name := range []string{"journal.db", "journal.db.incarnation", "tailscale"} {
		if _, err := os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Fatal("unexpected old runtime state", name)
		}
	}
}

func TestSeedRejectsArchiveTargetAndUnboundHostsBeforeOpeningStore(t *testing.T) {
	root := t.TempDir()
	clock := testkit.NewClock()
	seed := archive.StaticSeed{FormatVersion: 1, SourceDeploymentID: "old", Hosts: []archive.HostSetting{{HostID: "runner-a"}}}
	options := archiveseed.Options{Target: filepath.Join(root, "inside"), ArchiveRoot: root, Seed: seed, Now: clock.Now, IDs: testkit.NewIDGen(clock, 884), Random: strings.NewReader(strings.Repeat("x", 128))}
	if _, err := archiveseed.Initialize(t.Context(), options); err == nil {
		t.Fatal("archive target accepted")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("rejected seed modified archive", entries, err)
	}
	options.Target = filepath.Join(t.TempDir(), "unbound")
	if _, err := archiveseed.Initialize(t.Context(), options); err == nil {
		t.Fatal("unbound host accepted")
	}
	if _, err := os.Stat(options.Target); !os.IsNotExist(err) {
		t.Fatal("invalid binding created database")
	}
	file := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(file, []byte(`{"format_version":1,"source_deployment_id":"old","tasks":[{"task_id":"history"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var candidate archive.StaticSeed
	if err := archive.ReadJSON(file, &candidate); err == nil {
		t.Fatal("business field accepted in static seed")
	}
}

func TestRelativeArchiveTargetIsRejectedWithoutChangingArchive(t *testing.T) {
	working := t.TempDir()
	t.Chdir(working)
	if err := os.Mkdir("archive", 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("immutable archive content")
	if err := os.WriteFile(filepath.Join("archive", "snapshot.sqlite"), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	clock := testkit.NewClock()
	_, err := archiveseed.Initialize(t.Context(), archiveseed.Options{ArchiveRoot: "archive", Target: filepath.Join("archive", "new"), Seed: archive.StaticSeed{FormatVersion: 1, SourceDeploymentID: "old"}, Now: clock.Now, IDs: testkit.NewIDGen(clock, 889), Random: bytes.NewReader(bytes.Repeat([]byte{8}, 32))})
	if err == nil {
		t.Error("relative archive accepted a nested seed target")
	}
	entries, err := os.ReadDir("archive")
	if err != nil || len(entries) != 1 || entries[0].Name() != "snapshot.sqlite" {
		t.Fatal("rejected seed changed archive entries", entries, err)
	}
	after, err := os.ReadFile(filepath.Join("archive", "snapshot.sqlite"))
	if err != nil || !bytes.Equal(sentinel, after) {
		t.Fatal("rejected seed changed archive bytes", err)
	}
}
