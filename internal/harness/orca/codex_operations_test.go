package orca

import (
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

func nativeItem(at int64, turn string, item map[string]any) string {
	return rootRecord(at, "event_msg", map[string]any{"type": "item_completed", "turn_id": turn, "item": item})
}
func childItem(at int64, id string, state any) string {
	return nativeItem(at, "turn-original", map[string]any{"type": "CollabAgentToolCall", "id": "child-receipt", "tool": "wait", "status": "completed", "sender_thread_id": "session-root", "receiver_thread_ids": []string{id}, "agents_states": map[string]any{id: state}})
}
func spawnedChild() string {
	return rootRecord(2500, "response_item", map[string]any{"type": "function_call", "name": "spawn_agent", "call_id": "spawn"}) +
		rootRecord(2600, "response_item", map[string]any{"type": "function_call_output", "call_id": "spawn", "output": "{\"agent_id\":\"child-original\"}"}) +
		childItem(2601, "child-original", "running")
}
func yieldedProcess() string {
	return rootRecord(2500, "response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "exec-original", "arguments": "{\"cmd\":\"long-running-work\",\"yield_time_ms\":1000}"}) +
		rootRecord(2600, "response_item", map[string]any{"type": "function_call_output", "call_id": "exec-original", "output": "Wall time: 1.0000 seconds\nProcess running with session ID 12345\nOutput:\n"})
}
func processItem(at int64, process, status string) string {
	return nativeItem(at, "turn-original", map[string]any{"type": "CommandExecution", "id": "exec-original", "process_id": process, "status": status, "exit_code": 0})
}
func stdinExit(at int64, process string) string {
	return rootRecord(at, "response_item", map[string]any{"type": "function_call", "name": "write_stdin", "call_id": "poll-original", "arguments": "{\"session_id\":" + process + ",\"chars\":\"\"}"}) +
		rootRecord(at+1, "response_item", map[string]any{"type": "function_call_output", "call_id": "poll-original", "output": "Wall time: 0.0001 seconds\nProcess exited with code 0\nOutput:\n"})
}
func TestCodexNativeAsyncOperationsRequireMatchingTerminalReceipt(t *testing.T) {
	for _, tc := range []struct{ name, start, wrong, end string }{
		{"child completed", spawnedChild(), childItem(6000, "other-child", map[string]any{"completed": nil}), childItem(6000, "child-original", map[string]any{"completed": "done"})},
		{"child errored", spawnedChild(), childItem(6000, "other-child", map[string]any{"errored": "failed"}), childItem(6000, "child-original", map[string]any{"errored": "failed"})},
		{"child shutdown", spawnedChild(), childItem(6000, "other-child", "shutdown"), childItem(6000, "child-original", "shutdown")},
		{"durable command", yieldedProcess(), processItem(6000, "other-process", "completed"), processItem(6000, "12345", "completed")},
		{"stdin receipt", yieldedProcess(), stdinExit(6000, "67890"), stdinExit(6000, "12345")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, w, a := managedCodexFixture(t)
			body := rootStart(2000, "turn-original", w.st.Said) + tc.start + rootEnd(5000, "turn-original")
			for _, extra := range []string{"", tc.wrong} {
				writeRoot(t, *a.ProviderSession, body+extra)
				got, done, err := w.step(t.Context())
				if err != nil || done || got.RootTurn.Operations != "pending" || backgroundSettled(got, Wait{Satisfied: true}) {
					t.Fatalf("active operation was completed: done=%v err=%v root=%+v", done, err, got.RootTurn)
				}
				if err := f.ws.ReleaseHistorical(t.Context(), "run_1", mustState(w.st), false, 7000); err == nil || errors.Is(err, harness.ErrHeld) {
					t.Fatalf("active operation historical release=%v", err)
				}
				if len(f.orca.terms) != 1 {
					t.Fatal("active operation terminal closed")
				}
			}
			// A late receipt ends the same operation, without a new user turn or replay.
			writeRoot(t, *a.ProviderSession, body+tc.end)
			got, done, err := w.step(t.Context())
			if err != nil || !done || got.RootTurn.Operations != "settled" {
				t.Fatalf("matching terminal receipt did not settle: done=%v err=%v root=%+v", done, err, got.RootTurn)
			}
			if err := f.ws.ReleaseHistorical(t.Context(), "run_1", mustState(w.st), false, 7000); err != nil {
				t.Fatal(err)
			}
			if len(f.orca.terms) != 0 || f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 0 {
				t.Fatal("observation replayed or retained completed execution")
			}
		})
	}
}
func TestCodexNativeUnknownChildIsNotStoppedProof(t *testing.T) {
	for _, state := range []any{"not_found", "unknown", nil, "interrupted"} {
		t.Run("unknown", func(t *testing.T) {
			_, w, a := managedCodexFixture(t)
			writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+spawnedChild()+childItem(4000, "child-original", state)+rootEnd(5000, "turn-original"))
			got, done, err := w.step(t.Context())
			if err != nil || done || got.RootTurn.Operations != "unknown" {
				t.Fatalf("unknown child counted stopped: done=%v err=%v root=%+v", done, err, got.RootTurn)
			}
		})
	}
}
func TestCodexNativeOutstandingOperationsSurviveManagedContinuation(t *testing.T) {
	for _, start := range []string{spawnedChild(), yieldedProcess()} {
		t.Run("continuation", func(t *testing.T) {
			f, w, a := managedCodexFixture(t)
			body := rootStart(2000, "turn-original", w.st.Said) + start + rootEnd(5000, "turn-original")
			writeRoot(t, *a.ProviderSession, body)
			if _, _, err := w.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.ws.opts.Now = func() time.Time { return time.UnixMilli(5500) }
			if err := w.send(t.Context(), "continue managed work"); err != nil {
				t.Fatal(err)
			}
			writeRoot(t, *a.ProviderSession, body+rootStart(6000, "turn-next", w.st.Said)+rootEnd(7000, "turn-next"))
			got, done, err := w.step(t.Context())
			if err != nil || done || got.RootTurn.Operations != "pending" {
				t.Fatalf("managed turn forgot an outstanding operation: done=%v err=%v root=%+v", done, err, got.RootTurn)
			}
		})
	}
}
func TestCodexExecReceiptDoesNotReadCommandStdout(t *testing.T) {
	process, exited := codexExecReceipt("Wall time: 0.0001 seconds\nProcess exited with code 0\nOutput:\nProcess running with session ID 12345\n")
	if process != "" || !exited {
		t.Fatal("command stdout became host process evidence")
	}
}

func TestCodexNativeDelayedYieldReceiptCannotReopenEndedCommand(t *testing.T) {
	_, w, a := managedCodexFixture(t)
	start := rootRecord(2500, "response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "exec-original", "arguments": "{}"})
	end := processItem(2600, "12345", "completed")
	receipt := rootRecord(2700, "response_item", map[string]any{"type": "function_call_output", "call_id": "exec-original", "output": "Process running with session ID 12345"})
	writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+start+end+receipt+rootEnd(5000, "turn-original"))
	if _, done, err := w.step(t.Context()); err != nil || !done {
		t.Fatalf("delayed yield overrode matching native end: done=%v err=%v", done, err)
	}
}

func TestCodexNativeSpawnReceiptCannotOverwriteObservedChildEnd(t *testing.T) {
	_, w, a := managedCodexFixture(t)
	call := rootRecord(2500, "response_item", map[string]any{"type": "function_call", "name": "spawn_agent", "call_id": "spawn"})
	receipt := rootRecord(2700, "response_item", map[string]any{"type": "function_call_output", "call_id": "spawn", "output": "{\"agent_id\":\"child-original\"}"})
	writeRoot(t, *a.ProviderSession, rootStart(2000, "turn-original", w.st.Said)+call+childItem(2600, "child-original", map[string]any{"completed": "done"})+receipt+rootEnd(5000, "turn-original"))
	if _, done, err := w.step(t.Context()); err != nil || !done {
		t.Fatalf("spawn receipt overrode matching child end: done=%v err=%v", done, err)
	}
}
