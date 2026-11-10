package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestScriptSessionReusesOneThreadPump(t *testing.T) {
	h := testkit.NewHarness()
	sessions := &scriptedSessions{harness: h, ctx: t.Context(), nativeRoot: t.TempDir()}
	session, err := sessions.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	thread, err := session.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		resumed, err := session.ResumeThread(t.Context(), thread.ID(), harness.ThreadConfig{})
		if err != nil || resumed != thread {
			t.Fatal("resume replaced the existing thread pump", resumed, err)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { h.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closed session retained an orphan thread pump")
	}
}

func TestScriptThreadOriginalMatchesDeliveredNativeFrames(t *testing.T) {
	h := testkit.NewHarness()
	root := t.TempDir()
	sessions := &scriptedSessions{harness: h, ctx: t.Context(), nativeRoot: root}
	session, err := sessions.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	native, err := session.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	thread := native.(*scriptedThread)
	original := harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: "turn", ItemID: "reply", Text: "persisted native reply", Raw: json.RawMessage(`{"type":"agent_message","text":"persisted native reply"}`)}
	thread.FakeThread.Emit(original)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case actual := <-thread.Events():
		if actual.Text != original.Text {
			t.Fatal(actual)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The consumer may immediately archive the path, before any next frame or
	// shutdown. It must already contain this thread's actual observed bytes.
	source := thread.RolloutPath()
	if filepath.Dir(source) != root {
		t.Fatal("native locator points at a restart snapshot", source)
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dec := json.NewDecoder(file)
	var header struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if err = dec.Decode(&header); err != nil || header.Type != "session_meta" || header.Payload.ID != thread.ID() {
		t.Fatal(header, err)
	}
	var frame struct {
		Type    string        `json:"type"`
		Payload harness.Event `json:"payload"`
	}
	if err = dec.Decode(&frame); err != nil || frame.Payload.Text != original.Text || string(frame.Payload.Raw) != string(original.Raw) {
		t.Fatal(frame, err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		t.Fatal("unexpected fabricated native frames", err)
	}
}
