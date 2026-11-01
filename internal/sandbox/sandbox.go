// Package sandbox supplies isolated scripted boundaries for the real service.
// It is used only by tend-sandbox and tests, never by tend-server or releases.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/serverkit"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

const localOwner = "sandbox-owner"
const hostID = "sandbox-online"

// These public development credentials have no relationship to production.
const hostToken = "sandbox-only-runner-token"
const model = "sandbox-script"

// Options contains only sandbox options; no production configuration is loaded.
type Options struct {
	Listen   string
	Data     string
	Reset    bool
	Scenario string
	// FastPacing keeps scripted activity near-zero for automated tests.
	FastPacing bool
	// Listener allows loopback tests to reserve a port without a bind race.
	Listener net.Listener
	// Ready runs after seeding, before serving, for test synchronization.
	Ready func(*serverkit.Runtime, Demo)
	// replyDelta is a sandbox-test synchronization point after the first delta.
	replyDelta func(context.Context, harness.TurnInput)
	// runnerTransport observes real loopback requests in lifecycle tests.
	runnerTransport http.RoundTripper
	// Test-only seed boundaries keep broad lifecycle tests lightweight.
	turnInput func(harness.TurnInput) harness.TurnInput
	skipStory bool
	storyOnly bool
}

// Run owns all serving and runner goroutines until context cancellation.
func Run(ctx context.Context, options Options, log *slog.Logger) error {
	if options.Scenario == "" {
		options.Scenario = "demo"
	}
	if options.Scenario != "demo" && options.Scenario != "inbox" {
		return fmt.Errorf("unknown sandbox scenario %q", options.Scenario)
	}
	root, release, err := prepareData(options.Data, options.Reset)
	if err != nil {
		return err
	}
	defer release()
	listener := options.Listener
	if listener == nil {
		addr := options.Listen
		if addr == "" {
			addr = "127.0.0.1:8790"
		}
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			return err
		}
	}
	defer listener.Close()
	// Runner traffic always stays on loopback, even when the owner listener is
	// deliberately exposed through a development proxy or another interface.
	runnerListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer runnerListener.Close()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	h := testkit.NewHarness()
	h.ThreadPrefix = protocol.NewULIDGen(time.Now, nil).New(protocol.PrefixLaunch) + "_"
	historyPath := filepath.Join(root, "harness.json")
	if raw, readErr := os.ReadFile(historyPath); readErr == nil {
		if err := h.RestoreHistory(raw); err != nil {
			return fmt.Errorf("restore sandbox native history: %w", err)
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	h.RolloutPath = historyPath
	h.Models = []harness.Model{{ID: model, Name: model, Label: "沙盒脚本", Default: true}}
	h.Defaults = harness.ModelSelection{Model: model}
	sessions := &scriptedSessions{harness: h, ctx: runCtx}
	var assembled *serverkit.Runtime
	var seeding atomic.Bool
	seeding.Store(true)
	h.Turn = func(th *testkit.FakeThread, turn string, input harness.TurnInput) string {
		if options.turnInput != nil {
			input = options.turnInput(input)
		}
		return sessions.play(th, turn, input, func(ctx context.Context, th *testkit.FakeThread, turn string, input harness.TurnInput) string {
			return scriptedTurn(ctx, assembled, th, turn, input, log, options.replyDelta, sessions.scripts, pacing(options.FastPacing || seeding.Load()))
		})
	}
	named := hostedConfiguration()
	cfg := serverkit.Config{DataDir: root, LocalOwner: localOwner, Owners: []string{localOwner},
		MainAgent: &serverkit.MainAgentConfig{Named: &named, BackgroundConcurrency: 4},
		Repos:     []app.RepoConfig{{Repo: "sandbox-web", RemoteURL: "https://example.invalid/web.git"}, {Repo: "sandbox-service", RemoteURL: "https://example.invalid/service.git"}, {Repo: "sandbox-docs", RemoteURL: "https://example.invalid/docs.git"}},
		Projects:  []app.ProjectConfig{{Name: "demo-web", Title: "界面体验", Repos: []string{"sandbox-web"}}, {Name: "demo-service", Title: "服务可靠性", Repos: []string{"sandbox-service"}}, {Name: "demo-docs", Title: "文档与交付", Repos: []string{"sandbox-docs"}}},
		Hosts:     []app.HostConfig{{HostID: hostID, OS: "linux", Arch: "amd64", Repos: []string{"sandbox-web", "sandbox-service", "sandbox-docs"}, Slots: 4, TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:local"}, Worker: protocol.WorkerAppServer}, {HostID: "sandbox-offline", OS: "darwin", Arch: "arm64", Repos: []string{"sandbox-web"}, Slots: 2, TokenHash: runnerapi.HashToken("sandbox-offline-token"), Nodes: []string{"sandbox-offline"}}},
	}
	deps := serverkit.Dependencies{
		Listeners: []net.Listener{listener, runnerListener},
		WhoIs:     netx.Static{Identity: &netx.Identity{Node: "local", User: localOwner, Tags: []string{"tag:local"}}},
		OpenSession: func(ctx context.Context, spec serverkit.SessionSpec) (harness.Session, error) {
			return sessions.open(ctx)
		},
		Configure:  seedCategories,
		Initialize: func(_ context.Context, runtime *serverkit.Runtime) error { assembled = runtime; return nil },
		Background: func(ctx context.Context, runtime *serverkit.Runtime) error {
			j, err := journal.Open(ctx, filepath.Join(root, "runner", "journal.db"), time.Now)
			if err != nil {
				return err
			}
			defer j.Close()
			transport := &http.Transport{} // No environment proxy or external endpoint.
			defer transport.CloseIdleConnections()
			var clientTransport http.RoundTripper = transport
			if options.runnerTransport != nil {
				clientTransport = options.runnerTransport
			}
			instance := runtime.IDs.New(protocol.Prefix("rni"))
			executions := &scriptedExecutions{sessions: sessions}
			r := runner.New(runner.Config{InstanceID: instance, ServerURL: "http://" + runnerListener.Addr().String(), HostID: hostID, HostToken: hostToken, Version: "sandbox",
				Executions: executions, AgentInventory: executions.configuration,
				Journal: j, HTTPClient: &http.Client{Transport: clientTransport}, Worker: protocol.WorkerAppServer,
				Workers: &scriptedWorkers{pacing: pacing(options.FastPacing), fastSeed: seeding.Load, generation: func(ctx context.Context, id string) (int64, error) {
					var generation int64
					err := runtime.Store.Read(ctx, func(tx *store.ReadTx) error {
						return tx.Q().QueryRowContext(ctx, `SELECT generation FROM runs WHERE run_id=?`, id).Scan(&generation)
					})
					return generation, err
				}}, Workspaces: workspaces{root: filepath.Join(root, "workspaces")},
				Now: time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: log,
				RetryDelay: 50 * time.Millisecond, EventFlush: 50 * time.Millisecond, HeartbeatInterval: time.Second,
			})
			runnerCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- r.Run(runnerCtx) }()
			defer func() {
				stop()
				<-done
				// Run has joined every scripted worker: no process-external terminal
				// exists. Persist stopped facts for restart without inventing a result.
				proofCtx, proofCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer proofCancel()
				runs, proofErr := j.Runs(proofCtx)
				if proofErr == nil {
					for _, run := range runs {
						if run.Assignment.Role != protocol.RoleImplement && run.Assignment.Role != protocol.RoleAccept {
							continue
						}
						if proofErr = j.ConfirmStopped(proofCtx, run.Assignment.RunID); proofErr != nil {
							break
						}
					}
				}
				if proofErr != nil {
					log.Error("scripted shutdown proof could not be persisted", "err", proofErr)
				}
			}()
			readyCtx, readyCancel := context.WithTimeout(ctx, 2*time.Minute)
			defer readyCancel()
			for {
				ready := false
				err := runtime.Store.Read(readyCtx, func(tx *store.ReadTx) error {
					return tx.Q().QueryRowContext(readyCtx, `SELECT EXISTS(SELECT 1 FROM host_runtime r JOIN hosts h USING(host_id) WHERE h.host_id=? AND h.workspace_protocol>=1 AND r.instance_id=?)`, hostID, instance).Scan(&ready)
				})
				if err != nil {
					return err
				}
				if ready {
					log.Info("sandbox runner registered", "instance", instance)
					break
				}
				select {
				case err := <-done:
					done <- err // Preserve the result for the joining cleanup.
					if err == nil {
						return fmt.Errorf("sandbox runner stopped before registration")
					}
					return fmt.Errorf("sandbox runner startup: %w", err)
				case <-readyCtx.Done():
					return readyCtx.Err()
				case <-time.After(25 * time.Millisecond):
				}
			}
			var demo Demo
			if options.Scenario == "inbox" {
				demo, err = seedInbox(ctx, runtime, root)
			} else {
				demo, err = seedDemo(ctx, runtime, root, options.skipStory, options.storyOnly)
			}
			if err != nil {
				return err
			}
			seeding.Store(false)
			log.Info("sandbox ready", "addr", listener.Addr().String(), "scenario", options.Scenario, "runner_instance", instance)
			if options.Ready != nil {
				options.Ready(runtime, demo)
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	log.Info("sandbox starting", "addr", listener.Addr().String(), "scenario", options.Scenario)
	err = serverkit.Run(runCtx, cfg, []byte("sandbox-only-server-secret-32-bytes"), deps, log)
	cancel()
	h.Wait()
	history, historyErr := h.ExportHistory()
	if historyErr == nil {
		historyErr = os.WriteFile(historyPath+".tmp", history, 0600)
	}
	if historyErr == nil {
		historyErr = os.Rename(historyPath+".tmp", historyPath)
	}
	if historyErr != nil {
		return fmt.Errorf("persist sandbox native history: %w", historyErr)
	}
	if errors.Is(err, context.Canceled) {
		return ctx.Err()
	}
	return err
}

func hostedConfiguration() hostedconfig.Configuration {
	return hostedconfig.Configuration{Revision: 1, EnvironmentID: "sandbox-hosted", MainAgentID: "main", Definitions: agentconfig.Config{FormatVersion: 1,
		Runtimes: []agentconfig.RuntimeBinding{{ID: "script", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "sandbox-script-never-executed", DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation}, Enabled: true}},
		Agents:   []agentconfig.AgentDefinition{{LocalID: "main", Revision: 1, DisplayName: "沙盒协调 Agent", RuntimeID: "script", Categories: []string{"sandbox-planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: model, Enabled: true}, {LocalID: "manager", Revision: 1, DisplayName: "沙盒负责人 Agent", RuntimeID: "script", Categories: []string{protocol.DefaultIssueManagerCategory}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: model, Enabled: true}, {LocalID: "consultant", Revision: 1, DisplayName: "沙盒咨询 Agent", RuntimeID: "script", Categories: []string{"sandbox-planner"}, Purposes: []agentconfig.Purpose{agentconfig.Consultation}, Model: model, Enabled: true}},
	}}
}
