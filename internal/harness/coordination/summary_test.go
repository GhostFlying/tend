package coordination

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type summarySession struct {
	permissionSession
	forkConfig harness.ThreadConfig
	released   string
}

func (s *summarySession) ForkThread(ctx context.Context, _ string, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.forkConfig = cfg
	return s.StartThread(ctx, cfg)
}
func (s *summarySession) ReleaseThread(_ context.Context, id string) error {
	s.released = id
	return nil
}

func TestPrivatePersistentSummaryKeepsOwningThreadUsable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	native, err := testkit.NewHarness().Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session := &summarySession{permissionSession: permissionSession{native.(*testkit.FakeSession)}}
	stopped := ""
	driver := &Driver{NativeOwnerTools: true, Session: session, Profile: protocol.ExecutionProfile{Model: "model"}, Emit: func(context.Context, Event) error { return nil }, OnUsageStopped: func(_ context.Context, id string) error { stopped = id; return nil }}
	defer func() {
		cancel()
		_, _ = driver.Handle(context.Background(), "close", json.RawMessage(`{}`))
	}()
	call := func(method string, request Request) (any, error) {
		raw, _ := json.Marshal(request)
		return driver.Handle(ctx, method, raw)
	}
	cfg := harness.ThreadConfig{Purpose: NativeOwnerPurpose, Model: "model", Sandbox: "read-only", ApprovalPolicy: "never", Config: Isolation()}
	root, err := call("start", Request{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	rootID := root.(ThreadInfo).ID
	cfg.Purpose = "internal_summary"
	fork, err := call("fork", Request{ThreadID: rootID, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	forkID := fork.(ThreadInfo).ID
	if session.forkConfig.Ephemeral || session.forkConfig.LegacyRawUsage || forkID == rootID {
		t.Fatal("summary did not use a separate durable native thread", session.forkConfig)
	}
	if session.forkConfig.Sandbox != "read-only" || session.forkConfig.ApprovalPolicy != "never" || session.forkConfig.Config["features.shell_tool"] != false || len(session.forkConfig.Tools) != 0 {
		t.Fatal("private fork inherited ordinary owner native tools", session.forkConfig)
	}
	stage, err := call("start", Request{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	stageConfig := driver.threads[stage.(ThreadInfo).ID].(permissionThread).Config
	if stageConfig.Sandbox != "read-only" || stageConfig.ApprovalPolicy != "never" || stageConfig.Config["features.shell_tool"] != false || len(stageConfig.Tools) != 0 || stageConfig.Model != "model" {
		t.Fatal("private summary stage inherited ordinary owner native tools", stageConfig)
	}
	if _, err = call("release", Request{ThreadID: rootID}); err == nil {
		t.Fatal("released owning thread as a private summary")
	}
	if _, err = call("release", Request{ThreadID: forkID}); err != nil || stopped != forkID || session.released != forkID {
		t.Fatal(stopped, session.released, err)
	}
	if _, err = call("turn", Request{ThreadID: rootID, Input: harness.TurnInput{Text: "continue owning work"}}); err != nil {
		t.Fatal("summary release interrupted owning thread", err)
	}
}
