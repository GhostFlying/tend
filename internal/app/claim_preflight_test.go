package app

import (
	"context"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIdleClaimUsesReadConnectionWhileWriterIsOccupied(t *testing.T) {
	h := newHarness(t)
	h.hello("linux-1")
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 0}, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.store.Write(t.Context(), func(*store.Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	op, err := h.app.Execute(ctx, runner("linux-1"), env, Claim{})
	if err != nil || op.Status != protocol.OpSucceeded || string(op.Result) != `{"assignment":null}` {
		t.Fatal("idle poll acquired busy writer", op, err)
	}
}
