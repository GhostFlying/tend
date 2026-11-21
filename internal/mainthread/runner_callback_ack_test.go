package mainthread

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// Pause after the real coordinator has consumed the tool result and before its
// next statement, the original event ACK. Native dispatch and ACK stay real.
type closingCallbackPause struct {
	*runnerTransport
	delivered chan struct{}
	callError chan error
	resume    chan struct{}
}

func (p *closingCallbackPause) Call(ctx context.Context, method string, input, output any) error {
	err := p.runnerTransport.Call(ctx, method, input, output)
	if method == "tool_result" {
		if err != nil {
			p.callError <- err
			return err
		}
		close(p.delivered)
		select {
		case <-p.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func TestFault_SessionCloseSettlesOriginalCallbackACKAfterWriterTimeout(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	const run = "receipt-reader-run"
	seedClosingReceiptMetadata(t, e, "completed", false)
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		now := time.Now().UnixMilli()
		actor := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner@example.com", Client: "cli-review-fixture"}
		issue := protocol.Issue{
			IssueControl: protocol.IssueControl{IssueID: e.ids.New(protocol.PrefixWorkItem), State: protocol.IssueActive, WorkStatus: protocol.IssueTodo,
				Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: actor.Subject}, AssignmentEpoch: 1, AssignmentState: "active"},
			Revision: 1, Plan: protocol.IssuePlan{Title: "Closing original responsibility", Project: "default", Intent: "Retain close and original tail", Stages: []protocol.IssueStage{}},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.SaveCanonicalIssue(ctx, issue, 0, store.IssueRevisionProvenance{Actor: actor, RequestID: e.ids.New(protocol.PrefixRequest), Reason: map[string]string{"reason": "original owner fixture"}}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_manager_tasks(task_id,issue_id,category_id,expected_epoch,assignment_epoch,conversation_id,host_id,state,request_id,created_at,updated_at) VALUES('task_cached',?,'investigation',1,1,?,'tail-fixture-host','assigned','fixture',?,?)`, issue.IssueID, "issue:"+issue.IssueID, now, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_conversations(conversation_id,work_item_id,created_at) VALUES(?,?,?)`, "issue:"+issue.IssueID, issue.IssueID, now); err != nil {
			return err
		}
		if err := tx.InsertSpec(ctx, "task_cached", 1, `{}`, "", now); err != nil {
			return err
		}
		const host = "tail-fixture-host"
		if err := tx.UpsertHost(ctx, store.HostRow{HostID: host, OS: "linux", Arch: "amd64", Slots: 1}, time.Now().UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET host_id=?,observed_state='running' WHERE run_id=?`, host, run); err != nil {
			return err
		}
		if err := tx.InsertLease(ctx, store.LeaseRow{LeaseID: "tail-fixture-lease", RunID: run, HostID: host, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, time.Now().UnixMilli()); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	target := protocol.Target{Type: "run", ID: run}
	execute := func(operation string, payload any) (protocol.Operation, error) {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return e.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{
			RequestID: e.ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw,
		}, app.ManagerNativeCommand{OperationName: operation})
	}
	call := func(operation string, payload any) protocol.Operation {
		t.Helper()
		op, err := execute(operation, payload)
		if err != nil || op.Error != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(operation, op, err)
		}
		return op
	}
	callError := make(chan error, 1)
	claim := func(method string) protocol.ManagerNativeRequest {
		t.Helper()
		for ctx.Err() == nil {
			wake := e.app.Changes().Wait()
			op := call(protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{})
			var command protocol.ManagerNativeRequest
			if err := json.Unmarshal(op.Result, &command); err != nil {
				t.Fatal(err)
			}
			if command.ID != "" {
				if command.Method != method {
					t.Fatal("unexpected native request", command, method)
				}
				return command
			}
			select {
			case err := <-callError:
				t.Fatal("coordinator tool_result did not complete", err)
			case <-wake:
			case <-time.After(20 * time.Millisecond):
			case <-ctx.Done():
			}
		}
		t.Fatal("native command was never queued", method)
		return protocol.ManagerNativeRequest{}
	}
	transport := &closingCallbackPause{runnerTransport: &runnerTransport{app: e.app, run: run}, delivered: make(chan struct{}), callError: callError, resume: make(chan struct{})}
	session := coordination.New(ctx, transport)
	defer func() { cancel(); <-session.Done() }()
	var invocations atomic.Int32
	resumed := make(chan error, 1)
	go func() {
		_, err := session.ResumeThread(ctx, "original-thread", harness.ThreadConfig{OnToolCall: func(handlerCtx context.Context, call harness.ToolCall) harness.ToolResult {
			invocations.Add(1)
			const output = "original result"
			staged, err := e.store.StageOriginals(handlerCtx, "original-tool-output", []byte(call.Arguments), []byte(output))
			if err != nil {
				t.Error(err)
				return harness.ToolResult{}
			}
			err = e.store.Write(staged, func(tx *store.Tx) error {
				id := e.ids.New(protocol.PrefixMessage)
				var conversation string
				if err := tx.Q().QueryRowContext(staged, "SELECT conversation_id FROM issue_manager_tasks WHERE task_id='task_cached'").Scan(&conversation); err != nil {
					return err
				}
				if _, err := tx.InsertMessage(staged, store.MessageRow{MessageID: id, ConversationID: conversation, Author: "agent", Kind: "tool_use", CreatedAt: time.Now().UnixMilli()}); err != nil {
					return err
				}
				return tx.PutToolUse(staged, store.ToolUseRow{MessageID: id, ThreadID: call.ThreadID, TurnID: call.TurnID, ItemID: call.CallID, Tool: call.Tool, Input: string(call.Arguments), Output: output, Status: "completed"})
			})
			if err != nil {
				t.Error(err)
				return harness.ToolResult{}
			}
			return harness.ToolResult{Success: true, Text: output}
		}})
		resumed <- err
	}()
	req := claim("resume")
	meta, _ := json.Marshal(coordination.ThreadInfo{ID: "original-thread"})
	call(protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: req.ID, Result: meta})
	select {
	case err := <-resumed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	body, _ := json.Marshal(coordination.Event{ThreadID: "original-thread", Call: &harness.ToolCall{ThreadID: "original-thread", TurnID: "original-turn", CallID: "original-call", Tool: "fixture_tool", Arguments: json.RawMessage("{}")}})
	call(protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 1, Body: body})
	tool := claim("tool_result")
	call(protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: tool.ID, Result: json.RawMessage("null")})
	select {
	case <-transport.delivered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	finished := make(chan error, 1)
	go func() { finished <- session.Close() }()
	closeReq := claim("close")
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	outcomes := make(chan store.WriteObservation, 3)
	store.WithWriteObserver(time.Now, 0, func(o store.WriteObservation) {
		if o.Source.Operation == "manager.event.ack" && o.Source.RunID == run && o.Source.EventSequence == 1 {
			outcomes <- o
			if o.Stage == "connection_wait" && o.Outcome == "deadline_exceeded" {
				unblock()
			}
		}
	})(e.store)
	held := make(chan error, 1)
	go func() {
		held <- e.store.Write(ctx, func(*store.Tx) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(transport.resume)
	select {
	case o := <-outcomes:
		t.Logf("original Run %s seq %d ACK stage=%s outcome=%s", o.Source.RunID, o.Source.EventSequence, o.Stage, o.Outcome)
		if o.Stage != "connection_wait" || o.Outcome != "deadline_exceeded" {
			t.Fatalf("expected the original ACK to time out before its transaction, got %s/%s", o.Stage, o.Outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case second := <-outcomes:
		t.Logf("same ACK retry stage=%s outcome=%s", second.Stage, second.Outcome)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	cursor, err := e.app.ManagerAppliedCursor(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := execute(protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeReq.ID, Result: json.RawMessage("null"), LastSequence: 1})
	t.Logf("applied=%d handler_calls=%d close_status=%s op_error=%v execute_error=%v", cursor, invocations.Load(), outcome.Status, outcome.Error, err)
	if cursor != 1 || invocations.Load() != 1 || err != nil || outcome.Status != protocol.OpSucceeded {
		t.Fatal("after the original handler and result have completed, its ACK must settle and unblock the original close")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
