package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/tools"
)

func TestNamedHostedMainUsesDefinitionForActualStartAndResume(t *testing.T) {
	h := testkit.NewHarness()
	h.Models = []harness.Model{{ID: "named-catalog", Name: "native-model", Provider: "native-provider", SupportedEfforts: []string{"high"}, DefaultEffort: "high"}}
	configs := make(chan harness.ThreadConfig, 2)
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		configs <- th.Config
		return completeNamedFixtureReview(t, th, turn)
	}
	e := newEnv(t, h)
	e.stop()
	prepared, err := hostedconfig.Prepare(hostedconfig.Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "main-agent", Definitions: agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "CHOSEN_EXECUTABLE", DeclaredPurposes: []agentconfig.Purpose{agentconfig.Main}, Enabled: true}}, Agents: []agentconfig.AgentDefinition{{LocalID: "main-agent", Revision: 1, DisplayName: "Planner", RuntimeID: "runtime", Categories: []string{"planner"}, Purposes: []agentconfig.Purpose{agentconfig.Main}, Model: "named-catalog", Effort: "high", Instructions: "PRIVATE_ROLE_INSTRUCTIONS", Enabled: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	execute := func(operation string, target protocol.Target, payload any, revision int64, cmd app.Command) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		op, err := e.app.Execute(t.Context(), owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: operation, Target: target, Payload: raw, ExpectedControlRevision: &revision}, cmd)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
	}
	// Named adoption keeps the previous settings readable, but they cannot
	// select a maintenance model absent from the named runtime catalog.
	execute("conversation.set_settings", protocol.Target{Type: "conversation"}, protocol.SetConversationSettingsPayload{Model: "absent-legacy-model", Effort: "low"}, 0, app.SetConversationSettings{Available: true, Models: []protocol.ConversationModel{{ID: "absent-legacy-model", SupportedEfforts: []string{"low"}}}})
	execute("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, 0, app.PutAgentCategory{})
	raw, _ := json.Marshal(map[string]any{"revision": prepared.Revision(), "hash": prepared.Hash()})
	zero := int64(0)
	op, err := e.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "hosted.configure", Target: protocol.Target{Type: "hosted_configuration"}, ExpectedControlRevision: &zero, Payload: raw}, app.AcceptHostedConfiguration{Prepared: prepared})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	e.app = app.New(e.store, e.ids, e.now, app.WithNotifier(e.signal), app.WithHostedConfiguration(prepared))
	cb := contextbuilder.New(e.app, e.store, e.ids, contextbuilder.Config{})
	factories := make(chan hostedconfig.Selection, 4)
	e.m = New(e.app, cb, tools.New(e.app, e.store, cb, quiet()), Config{Instance: "main", Thread: ThreadConfig(t.TempDir(), "UNUSED_LEGACY_MODEL"), Open: h.Open, OpenNamed: func(ctx context.Context, selected hostedconfig.Selection) (harness.Session, error) {
		factories <- selected
		return h.Open(ctx)
	}, RestartDelay: 20 * time.Millisecond, Log: quiet()})
	e.start()
	for i := 0; i < 2; i++ {
		if i == 1 {
			e.stop()
			e.start()
		}
		e.say("Read and acknowledge this owner instruction")
		var cfg harness.ThreadConfig
		select {
		case cfg = <-configs:
		case <-time.After(10 * time.Second):
			t.Fatal("named turn did not start")
		}
		if cfg.Model != "native-model" || cfg.Config["model_reasoning_effort"] != "high" || cfg.Config["model_provider"] != "native-provider" || !strings.Contains(cfg.DeveloperInstructions, "PRIVATE_ROLE_INSTRUCTIONS") {
			t.Fatalf("actual thread configuration did not consume the definition: %+v", cfg)
		}
		if cfg.Sandbox != "read-only" || cfg.ApprovalPolicy != "never" || cfg.Config["features.shell_tool"] != false || cfg.Config["features.apply_patch_freeform"] != false {
			t.Fatalf("named native execution was not disabled: %+v", cfg)
		}
		eventually(t, "named business turn completed", func() bool {
			return e.count(`SELECT COUNT(*) FROM main_turns WHERE status='completed' AND model='native-model' AND settings_revision=1`) == i+1
		})
	}
	e.m.mu.Lock()
	session, source := e.m.session, e.m.thread
	e.m.mu.Unlock()
	selection, err := e.m.summaryModel(t.Context(), session, source)
	if err != nil || selection.Model != "native-model" || selection.Provider != "native-provider" || selection.Effort != "high" {
		t.Fatalf("maintenance ignored active frozen named selection: %+v %v", selection, err)
	}
	forkSession := &handoffTestSession{Session: session, h: h}
	fork, forker, err := e.m.summaryFork(t.Context(), forkSession, source)
	if err != nil {
		t.Fatal(err)
	}
	defer forker.ReleaseThread(t.Context(), fork.ID())
	forkConfig := fork.(*handoffTestThread).Thread.(*testkit.FakeThread).Config
	if forkConfig.Model != selection.Model || forkConfig.Config["model_provider"] != selection.Provider || forkConfig.Config["model_reasoning_effort"] != selection.Effort || forkConfig.Sandbox != "read-only" || forkConfig.ApprovalPolicy != "never" || len(forkConfig.Tools) != 0 {
		t.Fatalf("summary fork configuration differs from frozen selection: %+v want %+v", forkConfig, selection)
	}
	starts := businessStarts(h)
	if len(starts) != 2 {
		t.Fatalf("business starts=%d", len(starts))
	}
	for _, in := range starts {
		if in.Model != "native-model" || in.Effort != "high" || in.ModelProvider != "native-provider" {
			t.Fatalf("actual turn ignored named selection: %+v", in)
		}
	}
	if e.count(`SELECT COUNT(*) FROM hosted_executions WHERE role='main'`) != 2 {
		t.Fatal("native turns did not preserve private execution snapshots")
	}
	select {
	case selected := <-factories:
		if selected.Runtime.ExecutableRef != "CHOSEN_EXECUTABLE" {
			t.Fatal("factory did not receive selected runtime")
		}
	default:
		t.Fatal("named runtime factory was not used")
	}
	settings, err := e.m.Settings(t.Context())
	if err != nil || !settings.ReadOnly || settings.Source != "named_definition" || settings.Model != "named-catalog" {
		t.Fatal(settings, err)
	}
}

// completeNamedFixtureReview acknowledges this transport fixture using only
// the named main's actual routing and logical review capabilities.
func completeNamedFixtureReview(t *testing.T, th *testkit.FakeThread, turn string) string {
	t.Helper()
	call := 0
	invoke := func(name string, args any, out any) bool {
		call++
		result := th.CallTool(context.Background(), turn, fmt.Sprintf("named-review-%d", call), name, args)
		var answer struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
		}
		if !result.Success || json.Unmarshal([]byte(result.Text), &answer) != nil || !answer.OK {
			t.Errorf("named fixture tool %s failed: %s", name, result.Text)
			return false
		}
		return out == nil || json.Unmarshal(answer.Result, out) == nil
	}
	var state protocol.LogicalTurnState
	if !invoke("conversation_turn_read", struct{}{}, &state) {
		return "failed"
	}
	for _, message := range state.Messages {
		if message.Disposition != "pending" {
			continue
		}
		if !invoke("conversation_associate_issues", map[string]any{
			"effect_seq": call, "target_id": message.MessageID, "expected_control_revision": message.Revision,
			"payload": protocol.SetIssueMessageAssociationsPayload{ContentRevision: message.ContentRevision, Disposition: "unassigned", IssueIDs: []string{}},
		}, nil) {
			return "failed"
		}
	}
	if !invoke("conversation_turn_read", struct{}{}, &state) {
		return "failed"
	}
	review := protocol.LogicalTurnCommit{Contexts: state.Contexts}
	for _, message := range state.Messages {
		review.Messages = append(review.Messages, protocol.LogicalTurnReview{MessageID: message.MessageID, ContentRevision: message.ContentRevision, AssociationRevision: message.Revision})
	}
	if !invoke("conversation_turn_commit", review, nil) {
		return "failed"
	}
	return "completed"
}
