package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

type scriptedWorkers struct {
	pacing     scriptPacing
	fastSeed   func() bool
	generation func(context.Context, string) (int64, error)
	mu         sync.Mutex
	active     map[string]*scriptedWorker
}
type scriptedWorker struct {
	events chan harness.WorkerEvent
	done   chan struct{}
	result harness.WorkerResult
	err    error
	cancel context.CancelFunc
}

func (w *scriptedWorker) Events() <-chan harness.WorkerEvent  { return w.events }
func (w *scriptedWorker) Wait() (harness.WorkerResult, error) { <-w.done; return w.result, w.err }

func (workers *scriptedWorkers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	if spec.OnThread != nil {
		if err := spec.OnThread("sandbox-" + spec.RunID); err != nil {
			return nil, fmt.Errorf("script thread: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &scriptedWorker{events: make(chan harness.WorkerEvent), done: make(chan struct{}), cancel: cancel}
	workers.mu.Lock()
	if workers.active == nil {
		workers.active = map[string]*scriptedWorker{}
	}
	workers.active[spec.RunID] = w
	workers.mu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			workers.mu.Lock()
			if workers.active[spec.RunID] == w {
				delete(workers.active, spec.RunID)
			}
			workers.mu.Unlock()
		}()
		defer close(w.done)
		defer close(w.events)
		var contract struct {
			Goal string `json:"goal"`
		}
		_ = json.Unmarshal(spec.Spec, &contract)
		fail := strings.Contains(contract.Goal, "失败")
		if strings.Contains(contract.Goal, "首次失败") {
			generation, err := workers.generation(ctx, spec.RunID)
			if err != nil {
				w.err = err
				return
			}
			fail = spec.Role == protocol.RoleImplement && generation == 1
		}
		lifecycle := func(kind, status string) bool {
			raw, _ := json.Marshal(map[string]any{"kind": kind, "thread_id": "sandbox-" + spec.RunID, "turn_id": spec.RunID, "data": map[string]string{"status": status}})
			select {
			case w.events <- harness.WorkerEvent{NativeID: "sandbox-" + kind, Kind: kind, Body: raw}:
				return true
			case <-ctx.Done():
				w.err = ctx.Err()
				return false
			}
		}
		if !lifecycle("turn.started", "running") {
			return
		}
		pace := workers.pacing
		if workers.fastSeed != nil && workers.fastSeed() {
			pace = pacing(true)
		}
		steps := []map[string]any{
			{"type": "reasoning", "text": "准备沙盒执行环境"},
			{"type": "mcpToolCall", "tool": "files_read", "arguments": map[string]string{"path": "示例输入"}, "result": "已核对示例输入与界面要求"},
			{"type": "commandExecution", "command": "sandbox-check", "aggregatedOutput": "已生成报告并核对交付；仅模拟命令，没有启动外部进程", "exitCode": 0},
			{"type": "agentMessage", "text": "示例检查已结束，正在保存交付报告。"},
		}
		for n, step := range steps {
			id := fmt.Sprintf("sandbox-progress-%d", n)
			emit := func(kind, status string) bool {
				data := make(map[string]any, len(step)+1)
				for key, value := range step {
					data[key] = value
				}
				data["status"] = status
				if status == "inProgress" {
					delete(data, "result")
					delete(data, "aggregatedOutput")
					delete(data, "exitCode")
				}
				raw, _ := json.Marshal(map[string]any{"kind": kind, "thread_id": "sandbox-" + spec.RunID, "turn_id": spec.RunID, "item_id": id, "data": data})
				select {
				case w.events <- harness.WorkerEvent{NativeID: id + "-" + status, Kind: kind, Body: raw}:
					return true
				case <-ctx.Done():
					w.err = ctx.Err()
					return false
				}
			}
			if !emit("item.started", "inProgress") || !delay(ctx, pace.workerStep) || !emit("item.completed", "completed") {
				w.err = ctx.Err()
				return
			}
			if n == 0 && spec.Role == protocol.RoleImplement && strings.Contains(contract.Goal, "问我") {
				if spec.AskOwner == nil {
					w.err = errors.New("sandbox owner questions unavailable")
					return
				}
				if _, err := spec.AskOwner(ctx, protocol.AskOwnerPayload{QuestionKey: "sandbox-layout", Question: "示例执行需要你选择：先检查手机还是桌面？", Evidence: "脚本已完成准备，选择将用于报告，不涉及真实环境。", Options: []string{"手机", "桌面"}}); err != nil {
					w.err = err
					return
				}
			}
		}
		if fail {
			_ = lifecycle("turn.completed", "failed")
			w.err = errors.New("scripted sandbox failure: no production operation was attempted")
			return
		}
		if spec.Role == protocol.RoleAccept {
			if !lifecycle("turn.completed", "completed") {
				return
			}
			w.result = harness.WorkerResult{Summary: "独立脚本检查了示例报告。", Verdict: &protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{"sandbox":"report checked independently"}`)}}
			return
		}
		report := "# 示例交付报告\n\n已完成输入检查、进展演示与结果核对。所有执行均由沙盒脚本模拟，没有联系真实模型、仓库或生产环境。\n"
		file := filepath.Join(spec.ScratchDir, "sandbox-report.md")
		if err := os.WriteFile(file, []byte(report), 0600); err != nil {
			w.err = err
			return
		}
		uploaded, err := spec.UploadFile(ctx, file, "sandbox-report.md")
		if err != nil {
			w.err = err
			return
		}
		if !lifecycle("turn.completed", "completed") {
			return
		}
		w.result = harness.WorkerResult{Summary: "已交付示例报告，等待独立验收。", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: report, Evidence: []string{"沙盒脚本输出"}}, Files: []protocol.ArtifactFile{uploaded}}}
	}()
	return w, nil
}

// Workspaces uses opaque run identities, never repository paths or git. All
// filesystem work remains inside the sandbox data directory.
type workspaces struct{ root string }

func (w workspaces) Prepare(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	if err := ctx.Err(); err != nil {
		return runner.Prepared{}, err
	}
	if !protocol.ValidID(protocol.PrefixRun, as.RunID) {
		return runner.Prepared{}, errors.New("invalid sandbox run identity")
	}
	dir := filepath.Join(w.root, as.RunID)
	scratch, state := filepath.Join(dir, "scratch"), filepath.Join(dir, "state")
	for _, path := range []string{scratch, state} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return runner.Prepared{}, err
		}
	}
	return runner.Prepared{Dir: dir, ScratchDir: scratch, StateDir: state, Report: protocol.WorkspaceReport{BackendID: as.Workspace.BackendID}}, nil
}
func (workspaces) Collect(ctx context.Context, _ protocol.Assignment) (protocol.ArtifactPayload, error) {
	return protocol.ArtifactPayload{}, ctx.Err()
}
func (workspaces) Finish(context.Context, protocol.Assignment) error        { return nil }
func (workspaces) Cleanup(context.Context, protocol.WorkspaceCleanup) error { return nil }

// Release proves that a scripted run has no writable goroutine. Unlike an
// external terminal, these workers cannot outlive their sandbox process.
func (workers *scriptedWorkers) Release(ctx context.Context, run, _ string) error {
	workers.mu.Lock()
	worker := workers.active[run]
	workers.mu.Unlock()
	if worker == nil {
		return nil
	}
	worker.cancel()
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (workers *scriptedWorkers) ReleaseHistorical(ctx context.Context, run, thread string, _ bool, _ int64) error {
	return workers.Release(ctx, run, thread)
}

// scriptedExecutions advertises a frozen sandbox-only local manager snapshot.
// Registration and claims still use the real runner transport and journal.
type scriptedExecutions struct {
	sessions *scriptedSessions
	workers  *scriptedWorkers
	spaces   workspaces
}

func (s *scriptedExecutions) configuration(context.Context) (protocol.LocalAgentConfiguration, error) {
	return sandboxManagerConfiguration(false)
}

// Retained v1 pins keep their original inventory bytes and execution identity.
func sandboxManagerConfiguration(legacy bool) (protocol.LocalAgentConfiguration, error) {
	projection := protocol.AgentInventoryProjection{FormatVersion: 2, ExecutionEnvironmentID: hostID, SourceKind: protocol.AgentSourceInline,
		Runtimes: []protocol.AgentInventoryRuntime{{RuntimeID: "script", Revision: 1, Kind: "appserver-worker", CLI: "codex", DeclaredPurposes: []string{"issue_manager", "worker_run"}, Enabled: true, Registration: "staged", Availability: protocol.AgentInventoryAvailability{Reason: "runtime_not_verified"}}},
		Agents:   []protocol.AgentInventoryDefinition{{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: hostID, LocalID: "manager"}, Revision: 1, DisplayName: "沙盒 Issue 负责人", Binding: protocol.AgentRuntimeReference{RuntimeID: "script", Revision: 1}, Categories: []string{protocol.DefaultIssueManagerCategory}, Purposes: []string{"issue_manager", "worker_run"}, RequestedModel: model, Enabled: true, Registration: "staged", Availability: protocol.AgentInventoryAvailability{Reason: "runtime_not_verified"}}},
	}
	revision, snapshot := uint64(2), "sandbox-owner-v2"
	if legacy {
		revision, snapshot = 1, "sandbox-manager-v1"
		projection.Runtimes[0].DeclaredPurposes = []string{"issue_manager"}
		projection.Agents[0].Purposes = []string{"issue_manager"}
	} else {
		reviewer := projection.Agents[0]
		reviewer.Identity.LocalID = "reviewer"
		reviewer.DisplayName = "沙盒独立验收"
		reviewer.Categories = []string{"sandbox-reviewer"}
		reviewer.Purposes = []string{"worker_run"}
		projection.Agents = append(projection.Agents, reviewer)
	}
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	return protocol.LocalAgentConfiguration{State: "accepted", Revision: revision, SnapshotID: snapshot, PublicSHA256: hash, Projection: &projection}, err
}
func (s *scriptedExecutions) Refresh(ctx context.Context) error { return ctx.Err() }
func (s *scriptedExecutions) Observe(ctx context.Context) ([]protocol.AgentExecutor, error) {
	config, err := s.configuration(ctx)
	if err != nil {
		return nil, err
	}
	agent := config.Projection.Agents[0]
	pin := protocol.RunExecutionPin{Purpose: "issue_manager", Identity: agent.Identity, AgentRevision: 1, Runtime: agent.Binding, InventoryRevision: int64(config.Revision), SnapshotID: config.SnapshotID, PublicSHA256: config.PublicSHA256, Worker: protocol.WorkerAppServer, Profile: protocol.ExecutionProfile{Model: model}}
	worker := pin
	worker.Purpose, worker.BackendID = "", "git"
	reviewer := worker
	reviewer.Identity = config.Projection.Agents[1].Identity
	return []protocol.AgentExecutor{{Available: true, Pin: pin}, {Available: true, Pin: worker}, {Available: true, Pin: reviewer}}, ctx.Err()
}
func (s *scriptedExecutions) Resolve(ctx context.Context, pin protocol.RunExecutionPin) (runner.ResolvedExecution, error) {
	executors, err := s.Observe(ctx)
	if err != nil {
		return runner.ResolvedExecution{}, err
	}
	for _, executor := range executors[1:] {
		if pin == executor.Pin {
			profile := pin.Profile
			return runner.ResolvedExecution{Workers: s.workers, Workspaces: s.spaces, Profile: &profile}, nil
		}
	}
	return runner.ResolvedExecution{}, errors.New("sandbox worker snapshot does not match the accepted pin")
}
func (s *scriptedExecutions) ResolveManager(ctx context.Context, pin protocol.RunExecutionPin) (runner.ManagerExecution, error) {
	executors, err := s.Observe(ctx)
	if err != nil {
		return runner.ManagerExecution{}, err
	}
	expected := executors[0].Pin
	if pin != expected {
		legacy, err := sandboxManagerConfiguration(true)
		if err != nil {
			return runner.ManagerExecution{}, err
		}
		expected.InventoryRevision, expected.SnapshotID, expected.PublicSHA256 = int64(legacy.Revision), legacy.SnapshotID, legacy.PublicSHA256
		if pin != expected {
			return runner.ManagerExecution{}, errors.New("sandbox manager snapshot does not match the accepted pin")
		}
	}
	return runner.ManagerExecution{Open: func(ctx context.Context, _ string) (harness.Session, error) { return s.sessions.open(ctx) }, Profile: pin.Profile}, nil
}
