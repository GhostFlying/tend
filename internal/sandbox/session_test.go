package sandbox

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestScriptSessionReusesOneThreadPump(t *testing.T) {
	h := testkit.NewHarness()
	sessions := &scriptedSessions{harness: h, ctx: t.Context()}
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
