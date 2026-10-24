package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

type mcpPipe struct {
	in      *io.PipeWriter
	answers chan rpcMessage
	done    chan error
}

func startMCPPipe(t *testing.T, state string) *mcpPipe {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	outReader, outWriter := io.Pipe()
	p := &mcpPipe{in: writer, answers: make(chan rpcMessage, 10), done: make(chan error, 1)}
	go func() {
		p.done <- ServeMCP(ctx, state, "run_1", reader, outWriter)
		_ = outWriter.Close()
	}()
	go func() {
		decoder := json.NewDecoder(outReader)
		for {
			var answer rpcMessage
			if decoder.Decode(&answer) != nil {
				return
			}
			p.answers <- answer
		}
	}()
	t.Cleanup(func() { cancel(); _ = writer.Close(); _ = outReader.Close() })
	return p
}

func (p *mcpPipe) send(t *testing.T, text string) {
	t.Helper()
	if _, err := io.WriteString(p.in, text+"\n"); err != nil {
		t.Fatal(err)
	}
}
func (p *mcpPipe) answer(t *testing.T) rpcMessage {
	t.Helper()
	select {
	case answer := <-p.answers:
		return answer
	case <-time.After(5 * time.Second):
		t.Fatal("no MCP answer")
		return rpcMessage{}
	}
}
func (p *mcpPipe) finish(t *testing.T) {
	t.Helper()
	_ = p.in.Close()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP did not finish")
	}
}

const askLine = `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ask_owner","arguments":{"question_key":"scope","question":"Which scope?","evidence":"Investigated scope"}}}`

func TestOwnerQuestionMCPCancellationAndEOFStopWait(t *testing.T) {
	for _, ending := range []string{"cancel", "eof"} {
		t.Run(ending, func(t *testing.T) {
			entered, ended := make(chan struct{}), make(chan struct{})
			path := socketPath(t)
			server, err := Listen(path, "token", []harness.ToolSpec{harness.AskOwnerToolSpec}, func(ctx context.Context, _ harness.ToolCall) harness.ToolResult {
				close(entered)
				<-ctx.Done()
				close(ended)
				return harness.ToolResult{Text: "cancelled"}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			state := writeState(t, State{Run: "run_1", Socket: path, Token: "token"})
			pipe := startMCPPipe(t, state)
			pipe.send(t, askLine)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("question did not enter handler")
			}
			if ending == "cancel" {
				pipe.send(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9,"reason":"owner stopped"}}`)
				pipe.answer(t)
			}
			pipe.finish(t)
			select {
			case <-ended:
			case <-time.After(5 * time.Second):
				t.Fatal("socket disconnect did not cancel server handler")
			}
		})
	}
}

func TestOnlyOwnerQuestionsReconnectAfterRunnerSocketReplacement(t *testing.T) {
	for _, name := range []string{harness.AskOwnerTool, harness.ResultTool} {
		t.Run(name, func(t *testing.T) {
			path := socketPath(t)
			entered := make(chan struct{})
			var first harness.ToolCall
			tool := harness.AskOwnerToolSpec
			if name == harness.ResultTool {
				tool = harness.ResultToolSpec
			}
			server, err := Listen(path, "old-token", []harness.ToolSpec{tool}, func(ctx context.Context, call harness.ToolCall) harness.ToolResult {
				first = call
				close(entered)
				<-ctx.Done()
				return harness.ToolResult{Text: "runner restarting"}
			})
			if err != nil {
				t.Fatal(err)
			}
			state := writeState(t, State{Run: "run_1", Socket: path, Token: "old-token"})
			pipe := startMCPPipe(t, state)
			pipe.send(t, strings.Replace(askLine, harness.AskOwnerTool, name, 1))
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first call did not arrive")
			}
			_ = server.Close()
			var replayed atomic.Int64
			replacement, err := Listen(path, "new-token", []harness.ToolSpec{tool}, func(_ context.Context, call harness.ToolCall) harness.ToolResult {
				replayed.Add(1)
				if call.CallID != first.CallID || string(call.Arguments) != string(first.Arguments) {
					t.Error("reconnect changed tool-call identity or arguments")
				}
				return harness.ToolResult{Success: true, Text: "persisted owner answer"}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer replacement.Close()
			data, _ := json.Marshal(State{Run: "run_1", Socket: path, Token: "new-token"})
			if err := os.WriteFile(state, data, 0o600); err != nil {
				t.Fatal(err)
			}
			answer := pipe.answer(t)
			body, _ := json.Marshal(answer.Result)
			if name == harness.AskOwnerTool {
				if replayed.Load() != 1 || !strings.Contains(string(body), "persisted owner answer") {
					t.Fatalf("question did not reconnect: %s", body)
				}
			} else if replayed.Load() != 0 || !strings.Contains(string(body), `"isError":true`) {
				t.Fatalf("ordinary submission was replayed: %s", body)
			}
			pipe.finish(t)
		})
	}
}

func TestOwnerQuestionReconnectCannotFollowAnotherRun(t *testing.T) {
	path := socketPath(t)
	state := writeState(t, State{Run: "run_2", Socket: path, Token: "another-token"})
	pipe := startMCPPipe(t, state)
	pipe.send(t, askLine)
	answer := pipe.answer(t)
	body, _ := json.Marshal(answer.Result)
	if !strings.Contains(string(body), "another run") {
		t.Fatalf("wrong run was followed: %s", body)
	}
	pipe.finish(t)
}

// Exercise shutdown before the listener's connection-closing AfterFunc runs:
// the cancelled handler must not serialize its application-error result.
func TestOwnerQuestionShutdownLeavesSocketExchangeUncertain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	server := &Server{ctx: ctx, token: "token", tools: []harness.ToolSpec{harness.AskOwnerToolSpec}, handle: func(ctx context.Context, _ harness.ToolCall) harness.ToolResult {
		close(entered)
		<-ctx.Done()
		return harness.ToolResult{Text: "runner restarting"}
	}}
	serverConn, client := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { defer close(done); server.serve(serverConn) }()
	request, _ := json.Marshal(request{Token: "token", Method: "call", Name: harness.AskOwnerTool, CallID: "owner"})
	if _, err := client.Write(append(request, '\n')); err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	response, err := io.ReadAll(client)
	if err != nil || len(response) != 0 {
		t.Fatalf("shutdown sent an application result instead of closing transport: %s %v", response, err)
	}
	<-done
}
