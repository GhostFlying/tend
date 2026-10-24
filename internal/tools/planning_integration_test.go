//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// TestIntegration_NaturalLanguagePlanning checks model-authored arguments against
// the real tool schemas and an isolated ledger, without any runner or dispatcher.
// The explicitly supplied binary may be a private gateway wrapper. Such a wrapper
// must use its own temporary native home and clean it after the process exits.
func TestIntegration_NaturalLanguagePlanning(t *testing.T) {
	bin := os.Getenv("TEND_TRAEX_BIN")
	if bin == "" {
		t.Skip("set TEND_TRAEX_BIN explicitly to authorize one real planning turn")
	}
	model := os.Getenv("TEND_TRAEX_MODEL")
	if model == "" {
		model = "GPT-5.6-Sol"
	}
	e := newPlanningEnv(t)
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	session, err := traex.Start(ctx, traex.Options{Bin: bin, Dir: cwd, Env: []string{"TEND_ADDON=off"}, Version: "planning-integration"})
	if err != nil {
		t.Fatal("start isolated planning app-server failed")
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error("close isolated planning app-server failed")
		}
	}()
	type callSummary struct {
		name       string
		success    bool
		code       protocol.Code
		diagnostic string
	}
	var mu sync.Mutex
	var calls []callSummary
	cfg := harness.ThreadConfig{
		Cwd: cwd, Model: model, Sandbox: "read-only", ApprovalPolicy: "never",
		DeveloperInstructions: `You are a planning dispatcher connected only to an isolated test ledger. Current project: default. Use the supplied tend tools to record the owner's requested plan. Read their actual schemas and tool results. Discover existing configuration through the ledger, not files or network. Preserve the owner's original background and both future commitments. Each planning write uses commit_seq 1, 2, and so on within this turn; SourceMessageID is supplied by the host. Link each new task to its work item revision and relevant stages. Do not run commands, inspect local files, launch workers, manufacture results, close the concern, or claim implementation happened. Return a brief confirmation after recording the plan.`,
		Tools:                 Specs(),
		Config: map[string]any{
			"features.shell_tool": false, "features.js_repl": false, "features.multi_agent": false, "features.multi_agent_v2": false,
			"web_search": "disabled", "sandbox_workspace_write.writable_roots": []string{}, "features.apply_patch_freeform": false,
			"features.code_mode_host": false, "features.code_mode": false, "features.code_mode_only": false,
			"features.browser_use": false, "features.browser_use_external": false, "features.in_app_browser": false,
			"features.computer_use": false, "features.image_generation": false, "features.plugins": false, "features.apps": false,
			"features.memories": false, "features.goals": false, "features.skill_search": false, "features.tool_suggest": false,
			"features.workspace_dependencies": false, "features.workspace_undo": false, "skills.bundled.enabled": false,
			"skills.include_instructions": false, "tools.update_plan.enabled": false, "tools.experimental_request_user_input.enabled": false,
		},
	}
	cfg.OnToolCall = func(ctx context.Context, call harness.ToolCall) harness.ToolResult {
		principal := hosted
		principal.Thread = call.ThreadID
		result := e.svc.Handle(ctx, Call{Principal: principal, Native: call.ThreadID + "/" + call.TurnID + "/" + call.CallID,
			SourceMessageID: "msg_planning_integration", Tool: call.Tool, Arguments: call.Arguments})
		var answer answerView
		summary := callSummary{name: call.Tool, success: result.Success}
		if json.Unmarshal([]byte(result.Text), &answer) != nil || answer.OK != result.Success {
			summary.success = false
		}
		if answer.Error != nil {
			summary.code = answer.Error.Code
			// Only this isolated synthetic fixture is logged; never provider credentials.
			diagnostic, _ := json.Marshal(answer.Error)
			summary.diagnostic = string(diagnostic) + " arguments=" + string(call.Arguments)
		}
		if call.ThreadID == "" || call.TurnID == "" || call.CallID == "" {
			summary.success = false
		}
		t.Logf("planning tool=%s success=%t code=%s", summary.name, summary.success, summary.code)
		if summary.diagnostic != "" {
			t.Logf("planning validation diagnostic: %s", summary.diagnostic)
		}
		mu.Lock()
		calls = append(calls, summary)
		mu.Unlock()
		return result
	}
	thread, err := session.StartThread(ctx, cfg)
	if err != nil {
		t.Fatal("create isolated planning thread failed")
	}
	turnID := ""
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = thread.Interrupt(cleanup, turnID)
	}()
	// No legal background-kind or stage-status spellings are supplied here. The
	// model must obtain them from the real tool schema, not a prepared JSON fixture.
	turnID, err = thread.StartTurn(ctx, harness.TurnInput{Text: `请帮我规划一个“设置入口优化”工作项，保留两条原始背景：“移动端设置入口重复”；“设计交付后仍需后续独立评审”。请原样保留这些背景，并安排“设计”和“后续评审”两个阶段，不要删去后续阶段。现在只创建一个关联到设计阶段的执行任务，要求交付设计报告和独立文件（report 与 files 两种交付都要），不要求代码提交。评审阶段先保留在计划中，不创建评审任务。请通过实际工具写入临时账本；不启动执行、不调用 runner，不合入、不部署。工作区可使用临时独立目录。`})
	if err != nil {
		t.Fatal("start isolated planning turn failed")
	}
	completed := false
	for !completed {
		select {
		case <-ctx.Done():
			t.Fatal("isolated planning turn timed out")
		case event, ok := <-thread.Events():
			if !ok {
				t.Fatal("isolated planning session closed before completion")
			}
			if event.TurnID != turnID {
				continue
			}
			if event.Tool != nil {
				registered := slices.ContainsFunc(cfg.Tools, func(spec harness.ToolSpec) bool { return spec.Name == event.Tool.Name })
				// These native discovery calls only enumerate the empty MCP configuration.
				readonlyDiscovery := event.Tool.Name == "traecli/list_mcp_resources" || event.Tool.Name == "traecli/list_mcp_resource_templates"
				if !registered && !readonlyDiscovery {
					t.Errorf("unexpected native tool: %s", event.Tool.Name)
				}
			}
			if event.Kind == harness.EventTurnCompleted {
				if event.TurnStatus != "completed" {
					t.Fatalf("planning turn status: %s", event.TurnStatus)
				}
				completed = true
			}
		}
	}
	mu.Lock()
	summaries := slices.Clone(calls)
	mu.Unlock()
	var createdWork, createdTask bool
	for _, summary := range summaries {
		if !summary.success {
			t.Errorf("planning tool failed: %s code=%s diagnostic=%s", summary.name, summary.code, summary.diagnostic)
		}
		createdWork = createdWork || summary.name == "work_items_create" && summary.success
		createdTask = createdTask || summary.name == "tasks_create" && summary.success
	}
	if !createdWork || !createdTask {
		t.Fatal("model did not successfully create both work item and task")
	}
	err = e.store.Read(ctx, func(tx *store.ReadTx) error {
		items, err := store.ListWorkItems(ctx, tx.Q(), "", "")
		if err != nil {
			return err
		}
		if len(items) != 1 {
			t.Fatalf("work item count=%d, want 1", len(items))
		}
		item := items[0]
		var background strings.Builder
		for _, fact := range item.Background {
			background.WriteString(fact.Text)
			background.WriteByte('\n')
		}
		for _, original := range []string{"移动端设置入口重复", "设计交付后仍需后续独立评审"} {
			if !strings.Contains(background.String(), original) {
				t.Error("original owner background was lost")
			}
		}
		if len(item.Stages) != 2 {
			t.Fatalf("stage count=%d, want 2", len(item.Stages))
		}
		var design, review *protocol.WorkItemStage
		for index := range item.Stages {
			stage := &item.Stages[index]
			if strings.Contains(stage.Title, "评审") {
				review = stage
			} else if strings.Contains(stage.Title, "设计") {
				design = stage
			}
		}
		if design == nil || review == nil || design.StageID == review.StageID {
			t.Fatal("design/review stages were not retained")
		}
		var taskID string
		if err := tx.Q().QueryRowContext(ctx, "SELECT task_id FROM tasks").Scan(&taskID); err != nil {
			return err
		}
		var count int
		if err := tx.Q().QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks").Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("task count=%d, want 1", count)
		}
		if !slices.Equal(design.TaskIDs, []string{taskID}) || len(review.TaskIDs) != 0 {
			t.Error("task is not linked only to the design stage")
		}
		task, err := store.GetTask(ctx, tx.Q(), taskID)
		if err != nil {
			return err
		}
		raw, err := store.GetSpec(ctx, tx.Q(), taskID, task.SpecVersion)
		if err != nil {
			return err
		}
		var taskSpec domain.Spec
		if err := json.Unmarshal([]byte(raw), &taskSpec); err != nil {
			return err
		}
		if !slices.Contains(taskSpec.Deliverables, "report") || !slices.Contains(taskSpec.Deliverables, "files") {
			t.Error("task lacks required report/files deliverables")
		}
		if err := tx.Q().QueryRowContext(ctx, "SELECT COUNT(*) FROM runs").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("run count=%d, want no execution", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal("read isolated planning ledger failed")
	}
	t.Log("isolated planning result: original background, two stages, one linked report/files task, no runs")
}
