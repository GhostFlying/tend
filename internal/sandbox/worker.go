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
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

type scriptedWorkers struct {
	mu     sync.Mutex
	active map[string]*scriptedWorker
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
		steps := []string{"准备沙盒执行环境", "检查示例输入与界面要求", "生成报告并核对交付"}
		for n, step := range steps {
			if !delay(ctx, 400*time.Millisecond) {
				w.err = ctx.Err()
				return
			}
			raw, _ := json.Marshal(map[string]string{"text": step})
			select {
			case w.events <- harness.WorkerEvent{NativeID: fmt.Sprintf("sandbox-progress-%d", n), Kind: "item.completed", Body: raw}:
			case <-ctx.Done():
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
			w.err = errors.New("scripted sandbox failure: no production operation was attempted")
			return
		}
		if spec.Role == protocol.RoleAccept {
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
	return runner.Prepared{Dir: dir, ScratchDir: scratch, StateDir: state}, nil
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
