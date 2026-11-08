//go:build linux

package serverkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestNamedSessionRoutesRunnerCodexAtExecutingNode(t *testing.T) {
	root := t.TempDir()
	clock := testkit.NewClock()
	ledger, err := store.Open(t.Context(), filepath.Join(root, "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	ids := testkit.NewIDGen(clock, 73)
	a := app.New(ledger, ids, clock.Now)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "test-owner", Client: "test-cli"}
	runner := protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "worker"}
	execute := func(ctx context.Context, p protocol.Principal, cmd app.Command, target protocol.Target, payload any, revision *int64) (protocol.Operation, error) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return protocol.Operation{}, err
		}
		op, err := a.Execute(ctx, p, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: target, Payload: raw, ExpectedControlRevision: revision}, cmd)
		if err == nil && op.Status != protocol.OpSucceeded {
			err = op.Error
		}
		return op, err
	}
	must := func(p protocol.Principal, cmd app.Command, target protocol.Target, payload any, revision *int64) protocol.Operation {
		t.Helper()
		op, err := execute(t.Context(), p, cmd, target, payload, revision)
		if err != nil {
			t.Fatal(cmd.Operation(), op, err)
		}
		return op
	}
	zero := int64(0)
	must(protocol.Principal{Kind: protocol.PrincipalSystem}, app.SyncConfig{}, protocol.Target{Type: "config"}, app.ConfigPayload{Hosts: []app.HostConfig{{HostID: "worker", OS: "linux", Arch: "amd64", Slots: 1, Worker: protocol.WorkerAppServer, TokenHash: strings.Repeat("ab", 32), Nodes: []string{"tag:test-worker"}}}}, nil)
	must(owner, app.PutAgentCategory{}, protocol.Target{Type: "category", ID: protocol.DefaultIssueManagerCategory}, protocol.PutAgentCategoryPayload{Title: "Coordination", State: "active"}, &zero)
	runtime := &protocol.RunnerRuntime{InstanceID: "runner-process", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, JournalVersion: 19, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityAgentInventory, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager}}
	must(runner, app.Hello{}, protocol.Target{Type: "host", ID: "worker"}, protocol.HelloPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: runtime}, nil)
	definitions, err := agentconfig.Validate("worker", agentconfig.Config{FormatVersion: 2, Runtimes: []agentconfig.RuntimeBinding{{ID: "codex-native", Revision: 1, Kind: agentconfig.AppServerWorker, CLI: "codex", ExecutableRef: "runner-owned-codex", DeclaredPurposes: []agentconfig.Purpose{agentconfig.IssueManager}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "coordinator", Revision: 1, DisplayName: "Coordinator", RuntimeID: "codex-native", Categories: []string{protocol.DefaultIssueManagerCategory}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: "runner-model", Enabled: true}}}, agentconfig.SourceReference{Kind: agentconfig.Inline})
	if err != nil {
		t.Fatal(err)
	}
	projection := definitions.Public()
	_, hash, err := definitions.CanonicalPublic()
	if err != nil {
		t.Fatal(err)
	}
	must(runner, app.RegisterAgentInventory{}, protocol.Target{Type: "host", ID: "worker"}, protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: 1, SnapshotID: "runner-inventory", PublicSHA256: hash, Projection: projection}, &zero)
	runtime.AgentExecutors = []protocol.AgentExecutor{{Available: true, Pin: protocol.RunExecutionPin{Purpose: "issue_manager", Identity: projection.Agents[0].Identity, AgentRevision: 1, Runtime: projection.Agents[0].Binding, InventoryRevision: 1, SnapshotID: "runner-inventory", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: "runner-model"}, Worker: protocol.WorkerAppServer}}}
	runtime.Sequence++
	must(runner, app.Hello{}, protocol.Target{Type: "host", ID: "worker"}, protocol.HelloPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: runtime}, nil)
	created := must(owner, app.CanonicalOwnerIssueCommand{Action: "create"}, protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Coordinate an existing Issue", Project: "default", Intent: "Use the chosen runner"}}, nil)
	var issue protocol.Issue
	if err := json.Unmarshal(created.Result, &issue); err != nil {
		t.Fatal(err)
	}
	must(owner, app.RequestIssueManager{}, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Delegate existing responsibility"}, &issue.Revision)
	claimed := must(runner, app.Claim{}, protocol.Target{Type: "host", ID: "worker"}, protocol.ClaimPayload{Worker: protocol.WorkerAppServer, WorkspaceProtocol: 1, Runtime: runtime}, nil)
	var claim struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(claimed.Result, &claim); err != nil || claim.Assignment == nil || claim.Assignment.Role != protocol.RoleIssueManager {
		t.Fatal("manager was not actually claimed", string(claimed.Result), err)
	}
	selected, named, err := a.NamedHostedSelection(t.Context(), "issue:"+issue.IssueID)
	if err != nil || !named || !selected.RunnerManaged || selected.RunnerRunID != claim.Assignment.RunID || selected.Runtime.ID != "codex-native" {
		t.Fatal("selection does not identify the actual claimed runner", selected, err)
	}
	cfg := MainAgentConfig{Instance: "server-coordinator", CodexEnvironments: &codexenv.Bindings{Directory: filepath.Join(root, "server-only-environments"), Runtimes: map[string]string{"codex-native": "../invalid-local-environment"}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// This is the production callback installed by hostedMainThread, with no
	// injected SessionOpener and no native/model executable on the server.
	opener := hostedNamedSessionOpener(a, cfg, filepath.Join(root, "server-data"), root, nil, nil, log)
	session, err := opener(t.Context(), selected)
	if err != nil {
		t.Fatalf("server-local mapping rejected the claimed runner: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	replies := make(chan error, 1)
	go func() {
		for {
			wake := a.Changes().Wait()
			op, err := execute(ctx, protocol.Principal{Kind: protocol.PrincipalRun, Subject: selected.RunnerRunID, Role: protocol.RoleIssueManager}, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}, protocol.Target{Type: "run", ID: selected.RunnerRunID}, struct{}{}, nil)
			if err != nil {
				replies <- err
				return
			}
			var request protocol.ManagerNativeRequest
			if err = json.Unmarshal(op.Result, &request); err != nil {
				replies <- err
				return
			}
			if request.ID == "" {
				select {
				case <-ctx.Done():
					replies <- ctx.Err()
					return
				case <-wake:
				}
				continue
			}
			var result json.RawMessage
			switch request.Method {
			case "models":
				result = json.RawMessage(`[{"ID":"runner-model","Name":"Runner supplied"}]`)
			case "close":
				result = json.RawMessage(`null`)
			default:
				replies <- fmt.Errorf("unexpected native method %s", request.Method)
				return
			}
			_, err = execute(ctx, protocol.Principal{Kind: protocol.PrincipalRun, Subject: selected.RunnerRunID, Role: protocol.RoleIssueManager}, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}, protocol.Target{Type: "run", ID: selected.RunnerRunID}, protocol.ManagerNativeReply{ID: request.ID, Result: result}, nil)
			if err != nil || request.Method == "close" {
				replies <- err
				return
			}
		}
	}()
	models, err := session.(harness.ModelCatalog).Models(ctx)
	if err != nil || len(models) != 1 || models[0].ID != "runner-model" {
		t.Errorf("real runner App transport did not supply the models: %+v %v", models, err)
	}
	if err := session.Close(); err != nil {
		t.Error(err)
	}
	if err := <-replies; err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(root, "server-data", "codex-coordinators")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("remote routing bound a server-local environment", err)
	}
	missing := selected
	missing.RunnerRunID = ""
	if session, err := opener(t.Context(), missing); err == nil || session != nil || !strings.Contains(err.Error(), "awaits its runner claim") {
		t.Fatalf("unclaimed selection routed elsewhere: %v %v", session, err)
	}
	local := hostedconfig.Selection{Runtime: agentconfig.RuntimeBinding{ID: "codex-native"}}
	local.Runtime.ExecutableRef, err = os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if session, err := opener(t.Context(), local); err == nil || session != nil || !strings.Contains(err.Error(), "invalid environment ID") {
		t.Fatalf("local environment validation was bypassed: %v %v", session, err)
	}
	if err := ledger.Read(t.Context(), func(tx *store.ReadTx) error {
		var total, original int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*),SUM(CASE WHEN run_id=? THEN 1 ELSE 0 END) FROM manager_native_requests`, selected.RunnerRunID).Scan(&total, &original); err != nil {
			return err
		}
		if total != 2 || original != 2 {
			return fmt.Errorf("transport did not stay on the original claim: total=%d original=%d", total, original)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

}
