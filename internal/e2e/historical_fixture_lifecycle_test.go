package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestHistoricalFixtureReviewStopsWithDriverContext(t *testing.T) {
	srv := newServer(t, time.Minute)
	fixture := testkit.NewHarness()
	session, err := fixture.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		session.Close()
		fixture.Wait()
	}()
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	thread, err := session.StartThread(t.Context(), harness.ThreadConfig{OnToolCall: func(ctx context.Context, _ harness.ToolCall) harness.ToolResult {
		entered <- ctx
		<-release
		return harness.ToolResult{Success: false, Text: "fixture driver stopped"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	driver, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan string, 1)
	go func() { done <- completeHistoricalFixtureReview(driver, srv, thread.(*testkit.FakeThread), "repair") }()
	toolContext := <-entered
	stop()
	close(release)
	status := <-done
	if toolContext.Err() != context.Canceled || status != "interrupted" {
		t.Fatalf("normal fixture stop: tool context=%v status=%s", toolContext.Err(), status)
	}
}
