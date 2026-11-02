package remote

import (
	"context"
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
