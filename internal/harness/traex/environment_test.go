package traex

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestEnvironmentHandshakeRejectsRedirectedStateHome(t *testing.T) {
	fake, session := newFake(t)
	session.environmentReference = &codexenv.Reference{Profile: codexenv.Profile{Home: t.TempDir()}}
	fake.on(methodInitialize, func(json.RawMessage) any { return map[string]string{"codexHome": t.TempDir()} })
	if err := session.initialize(t.Context(), "test"); err == nil {
		t.Fatal("native wrapper redirected the isolated home without rejection")
	}
}

func TestEnvironmentIdleRequiresSettledToolCallback(t *testing.T) {
	fake, session := newFake(t)
	fake.on("thread/read", func(raw json.RawMessage) any {
		var params struct {
			IncludeTurns bool `json:"includeTurns"`
		}
		if err := json.Unmarshal(raw, &params); err != nil || params.IncludeTurns {
			t.Error("environment adoption requested full history instead of live status")
		}
		return map[string]any{"thread": map[string]any{"id": "thread", "status": map[string]string{"type": "idle"}}}
	})
	release := make(chan struct{})
	session.register("thread", func(context.Context, harness.ToolCall) harness.ToolResult {
		<-release
		return harness.ToolResult{Success: true}
	})
	prepared := session.prepareServerRequest(methodToolCall, json.RawMessage(`{"threadId":"thread","turnId":"turn","callId":"call","tool":"fixture","arguments":{}}`))
	done := make(chan struct{})
	go func() { _, _ = prepared(t.Context()); close(done) }()
	idle, err := session.EnvironmentQuiescent(t.Context(), "thread")
	close(release)
	<-done
	if err != nil || idle {
		t.Fatal("idle status bypassed an unsettled callback", err)
	}
	idle, err = session.EnvironmentQuiescent(t.Context(), "thread")
	if err != nil || !idle {
		t.Fatal("settled callback continued to block idle adoption", err)
	}
}

func TestWorkerRestoresOriginalBindingBeforeNativeStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict environment requires Linux")
	}
	state := t.TempDir()
	original := &codexenv.Binding{Directory: t.TempDir(), ID: "original"}
	if _, err := codexenv.BindExecution(t.Context(), state, "run:retained", original, false); err != nil {
		t.Fatal(err)
	}
	for _, mapping := range []*codexenv.Binding{nil, {Directory: t.TempDir(), ID: "replacement"}} {
		workers := NewWorkers(WorkerOptions{Session: Options{Environment: mapping}})
		stopped := errors.New("stop before native startup")
		workers.start = func(_ context.Context, opts Options) (*Session, error) {
			if opts.Environment == nil || *opts.Environment != *original || opts.EnvironmentExecution != "run:retained" {
				t.Error("retained worker adopted current mapping", opts.Environment)
			}
			return nil, stopped
		}
		_, err := workers.Start(t.Context(), harness.WorkerSpec{RunID: "retained", StateDir: state, Dir: filepath.Dir(state), Thread: "original-thread"})
		if !errors.Is(err, stopped) {
			t.Fatal(err)
		}
	}
}

func TestQuestionContinuationRestoresPredecessorBinding(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict environment requires Linux")
	}
	previous, state := t.TempDir(), t.TempDir()
	original := &codexenv.Binding{Directory: t.TempDir(), ID: "original"}
	if _, err := codexenv.BindExecution(t.Context(), previous, "run:before", original, false); err != nil {
		t.Fatal(err)
	}
	workers := NewWorkers(WorkerOptions{Session: Options{Environment: &codexenv.Binding{Directory: t.TempDir(), ID: "replacement"}}})
	stopped := errors.New("stop before native startup")
	workers.start = func(_ context.Context, opts Options) (*Session, error) {
		if opts.Environment == nil || *opts.Environment != *original || opts.EnvironmentExecution != "run:after" {
			t.Error("question continuation lost original home", opts.Environment)
		}
		return nil, stopped
	}
	_, err := workers.Start(t.Context(), harness.WorkerSpec{RunID: "after", StateDir: state, Dir: t.TempDir(), Thread: "original-thread", PredecessorStateDir: previous, QuestionContinuation: &protocol.QuestionContinuation{RunID: "before", ThreadID: "original-thread"}})
	if !errors.Is(err, stopped) {
		t.Fatal(err)
	}
}
