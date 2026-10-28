//go:build integration

package mainthread

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type nativeObservedSession struct {
	harness.Session
	onTool func(context.Context, harness.ToolCall, harness.ToolHandler) harness.ToolResult
}

func (s nativeObservedSession) StartThread(ctx context.Context, cfg harness.ThreadConfig) (harness.Thread, error) {
	handler := cfg.OnToolCall
	cfg.OnToolCall = func(ctx context.Context, c harness.ToolCall) harness.ToolResult { return s.onTool(ctx, c, handler) }
	return s.Session.StartThread(ctx, cfg)
}
func (s nativeObservedSession) ResumeThread(ctx context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	handler := cfg.OnToolCall
	cfg.OnToolCall = func(ctx context.Context, c harness.ToolCall) harness.ToolResult { return s.onTool(ctx, c, handler) }
	return s.Session.ResumeThread(ctx, id, cfg)
}

// Real hosted Manager + dispatcher + Tend tool service + native app-server and
// real model. Only the first read-only tool's completion is held by the test;
// no production service, session, credentials, or runner is used.
func TestIntegrationHostedOwnerSteerRealModel(t *testing.T) {
	for _, mode := range []string{"clarification", "status", "stop"} {
		t.Run(mode, func(t *testing.T) { hostedOwnerSteerRealModel(t, mode) })
	}
}

func hostedOwnerSteerRealModel(t *testing.T, mode string) {
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	firstTool := make(chan harness.ToolCall, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls, opens atomic.Int32
	model := os.Getenv("TEND_TRAEX_MODEL")
	if model == "" {
		model = "GPT-5.6-Sol"
	}
	cwd := t.TempDir()
	e := newEnv(t, testkit.NewHarness(), func(cfg *Config) {
		cfg.Thread = ThreadConfig(cwd, model)
		cfg.Open = func(ctx context.Context) (harness.Session, error) {
			s, err := traex.Start(ctx, traex.Options{Dir: cwd, Env: []string{"TEND_ADDON=off"}, Log: quiet()})
			if err != nil {
				return nil, err
			}
			opens.Add(1)
			return nativeObservedSession{Session: s, onTool: func(ctx context.Context, c harness.ToolCall, h harness.ToolHandler) harness.ToolResult {
				if c.Tool == "repos_list" && calls.Add(1) == 1 {
					firstTool <- c
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				return h(ctx, c)
			}}, nil
		}
	})
	first := e.say("This is an isolated coordination test, no delegated execution is authorized. Read repos_list once. Then briefly report the registered repo and conclude with ORIGINAL_FINISH KEEP_GOAL. Keep the goal KEEP_GOAL throughout this request. Do not create tasks, work items, decisions or followups.")
	var call harness.ToolCall
	select {
	case call = <-firstTool:
	case <-ctx.Done():
		t.Fatal("native model did not call repos_list: ", ctx.Err())
	}
	t.Logf("original owner=%s thread=%s turn=%s tool=%s call=%s; native tool is still running", first, call.ThreadID, call.TurnID, call.Tool, call.CallID)
	marker := "STEER_FINISH"
	message := "Change the conclusion to STEER_FINISH. Preserve KEEP_GOAL and report the repo you just read."
	switch mode {
	case "status":
		marker = "STATUS_RECEIVED"
		message = "Status question: briefly tell me the current progress with STATUS_RECEIVED. This question preserves the original request; still conclude with ORIGINAL_FINISH KEEP_GOAL after the pending read completes."
	case "stop":
		marker = "STOP_CONFIRMED"
		message = "Stop preparing ORIGINAL_FINISH. Let the already-running read finish safely, then confirm STOP_CONFIRMED and KEEP_GOAL. Do not continue the original conclusion or cancel any worker."
	}
	second := e.say(message + " Briefly acknowledge this supplement. Do not call repos_list again. This message does not authorize new work.")
	await := func(what string, cond func() bool) {
		t.Helper()
		for !cond() {
			select {
			case <-ctx.Done():
				t.Fatal(what, ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	await("native acceptance", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND status='accepted'`, second) == 1
	})
	if e.count(`SELECT COUNT(*) FROM main_steers WHERE native_item_id<>''`) != 0 || e.count(`SELECT COUNT(*) FROM main_turns WHERE status='completed'`) != 0 {
		t.Fatal("input consumption or final was claimed during blocked tool")
	}
	t.Logf("steer owner=%s accepted in original turn while tool awaited completion; consumption not yet witnessed", second)
	close(release)
	await("original native turn completes", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id=? AND thread_id=? AND turn_id=? AND status='completed'`, first, call.ThreadID, call.TurnID) == 1
	})
	var body string
	_ = e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT body FROM conversation_messages WHERE author='main_agent' AND reply_to=? ORDER BY seq DESC LIMIT 1`, second).Scan(&body)
	})
	t.Logf("final same-turn reply=%q", body)
	if !strings.Contains(body, marker) || !strings.Contains(body, "KEEP_GOAL") || (mode == "status" && !strings.Contains(body, "ORIGINAL_FINISH")) {
		t.Fatal("native model did not adjust with original goal and source")
	}
	if e.count(`SELECT COUNT(*) FROM conversation_reply_sources s JOIN conversation_agent_messages a ON a.message_id=s.reply_id WHERE s.source_id=? AND a.thread_id=? AND a.turn_id=? AND a.status='completed'`, second, call.ThreadID, call.TurnID) == 0 {
		t.Fatal("reply did not retain the exact consumed owner source")
	}
	if calls.Load() != 1 || opens.Load() != 1 || e.count(`SELECT COUNT(*) FROM main_turns t JOIN logical_turn_executions e ON e.request_id=t.request_id WHERE e.repair=0`) != 1 || e.count(`SELECT COUNT(*) FROM main_steers WHERE message_id=? AND thread_id=? AND turn_id=? AND status IN ('consumed','completed') AND native_item_id<>''`, second, call.ThreadID, call.TurnID) != 1 {
		t.Fatal("duplicate tool, coordinator, turn, or missing native consumption proof")
	}
	if e.count(`SELECT COUNT(*) FROM tasks`) != 0 {
		t.Fatal("steer expanded execution authority")
	}
	t.Logf("proof: opens=%d repos_list=%d turns=1 consumed steer=1 task writes=0", opens.Load(), calls.Load())
	third := e.say("Idle-path check: reply only IDLE_FINISH. No tools or new work.")
	await("idle turn", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE message_id=? AND status='completed'`, third) == 1
	})
	if e.count(`SELECT COUNT(DISTINCT thread_id) FROM main_turns`) != 1 || opens.Load() != 1 {
		t.Fatal("idle input replaced native session")
	}
	t.Log(fmt.Sprintf("idle owner=%s began the second turn in the same native session; no pending work", third))
}
