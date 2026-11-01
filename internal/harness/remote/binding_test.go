package remote

import "testing"

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
