package remote

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
)

func TestQuestionSuccessorNeedsLedgerEdgeAndOriginalNativeBinding(t *testing.T) {
	cfg := BrokerConfig{Cwd: t.TempDir(), QuestionPredecessor: func(_ context.Context, run string) (string, error) {
		if run == "successor" {
			return "source", nil
		}
		return "", nil
	}}
	b := NewBroker(t.Context(), cfg, nil)
	defer b.Close()
	if err := b.saveBinding("source", nativeBinding{Cwd: "/workspace", Thread: "original"}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []string{"unrelated", "successor"} {
		for _, hello := range []Hello{{Cwd: "/workspace", ThreadID: "original"}, {Cwd: "/other", ThreadID: "original"}, {Cwd: "/workspace", ThreadID: "unrelated"}} {
			err := b.bindQuestionHello(t.Context(), run, hello)
			valid := run == "successor" && hello.Cwd == "/workspace" && hello.ThreadID == "original"
			if (err == nil) != valid {
				t.Fatal("wrong question continuation admission", run, hello, err)
			}
		}
	}
	restarted := NewBroker(t.Context(), BrokerConfig{Cwd: cfg.Cwd}, nil)
	defer restarted.Close()
	if err := restarted.bindHello("successor", Hello{Cwd: "/workspace", ThreadID: "original"}); err != nil {
		t.Fatal("authorized binding was not durable", err)
	}
}

func TestBindingSurvivesBrokerRestartAndRejectsUnrelatedHistory(t *testing.T) {
	cfg := BrokerConfig{Cwd: t.TempDir()}
	b := NewBroker(t.Context(), cfg, nil)
	defer b.Close()
	if err := b.bindHello("run", Hello{Cwd: "/workspace", ThreadID: "unrelated"}); err == nil {
		t.Fatal("an unbound run adopted existing center history")
	}
	if err := b.bindHello("run", Hello{Cwd: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := b.saveBinding("run", nativeBinding{Cwd: "/workspace", Pending: true}); err != nil {
		t.Fatal(err)
	}
	restarted := NewBroker(t.Context(), cfg, nil)
	defer restarted.Close()
	if err := restarted.bindHello("run", Hello{Cwd: "/workspace"}); err == nil {
		t.Fatal("an unanswered start became a new session after restart")
	}
	if err := b.saveBinding("run", nativeBinding{Cwd: "/workspace", Thread: "original"}); err != nil {
		t.Fatal(err)
	}
	for _, hello := range []Hello{{Cwd: "/workspace"}, {Cwd: "/other", ThreadID: "original"}, {Cwd: "/workspace", ThreadID: "unrelated"}} {
		if err := restarted.bindHello("run", hello); err == nil {
			t.Fatalf("changed binding accepted: %+v", hello)
		}
	}
	if err := restarted.bindHello("run", Hello{Cwd: "/workspace", ThreadID: "original"}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.bindHello("another-run", Hello{Cwd: "/workspace", ThreadID: "original"}); err == nil {
		t.Fatal("another run adopted the original run's history")
	}
}

func TestOptionalContinuationCanStartFreshWithoutLosingUnknownBinding(t *testing.T) {
	b := NewBroker(t.Context(), BrokerConfig{Cwd: t.TempDir(), QuestionPredecessor: func(_ context.Context, run string) (string, error) {
		if run == "next" {
			return "stopped", nil
		}
		return "", nil
	}}, nil)
	defer b.Close()
	hello := Hello{Cwd: "/workspace", ThreadID: "missing-history", OptionalContinuation: true}
	got, err := b.resolveContinuationHello(t.Context(), "next", hello)
	if err != nil || got.ThreadID != "" {
		t.Fatal("missing optional history blocked fresh execution", got, err)
	}
	// An unanswered start belongs to this new Run and must never be erased by
	// retrying its old optional predecessor handshake.
	if err := b.saveBinding("next", nativeBinding{Cwd: hello.Cwd, Pending: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.resolveContinuationHello(t.Context(), "next", hello); err == nil {
		t.Fatal("unknown current start was replaced")
	}
	if _, err := b.resolveContinuationHello(t.Context(), "unrelated", hello); err == nil {
		t.Fatal("unrelated history granted continuation")
	}
}
func TestOnlyDefiniteOptionalResumeRejectionAllowsFreshStart(t *testing.T) {
	b := NewBroker(t.Context(), BrokerConfig{Cwd: t.TempDir()}, nil)
	defer b.Close()
	run := &nativeRun{cwd: "/workspace", thread: "old", optionalContinuation: true}
	if err := b.saveBinding("next", nativeBinding{Cwd: run.cwd, Thread: run.thread}); err != nil {
		t.Fatal(err)
	}
	_, err := constrainControl([]byte(`{"id":5,"method":"thread/resume","params":{"threadId":"old"}}`), run, protocol.DesiredRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.settleContinuationResume("next", run, nativeFrame{ID: json.RawMessage(`4`), Error: json.RawMessage(`{"code":-32602}`)}); err != nil || run.thread != "old" {
		t.Fatal("unrelated rejection erased binding", err)
	}
	if err := b.settleContinuationResume("next", run, nativeFrame{ID: json.RawMessage(`5`), Error: json.RawMessage(`{"code":-32602}`)}); err != nil || run.thread != "" {
		t.Fatal("definite missing-history response blocked fresh session", err)
	}
	if _, err := constrainControl([]byte(`{"id":6,"method":"thread/start","params":{}}`), run, protocol.DesiredRun); err != nil {
		t.Fatal(err)
	}
}
