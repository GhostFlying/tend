package mainthread

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestSettingsUseResolvedDefaultsAndApplyOnlyToNextTurn(t *testing.T) {
	h := testkit.NewHarness()
	h.Models = []harness.Model{
		{ID: "a", Name: "raw-a", Provider: "provider-a", Label: "A", DefaultEffort: "low", SupportedEfforts: []string{"low", "medium"}},
		{ID: "b", Name: "raw-b", Provider: "provider-b", Label: "B", Default: true, DefaultEffort: "high", SupportedEfforts: []string{"high"}},
	}
	// The current thread uses A/medium even though the catalog's default is B.
	h.Defaults = harness.ModelSelection{Model: "raw-a", Provider: "provider-a", Effort: "medium"}
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.Turn = func(_ *testkit.FakeThread, _ string, in harness.TurnInput) string {
		if strings.HasSuffix(in.Text, "\nfirst") {
			close(firstStarted)
			<-release
		}
		return "completed"
	}
	e := newEnv(t, h)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	eventually(t, "catalog and actual defaults are ready", func() bool {
		settings, err := e.m.Settings(context.Background())
		return err == nil && settings.Available && settings.Revision == 0 && settings.Model == "a" && settings.Effort == "medium"
	})
	e.say("first")
	eventually(t, "first turn starts", func() bool {
		select {
		case <-firstStarted:
			return true
		default:
			return false
		}
	})
	payload, _ := json.Marshal(protocol.SetConversationSettingsPayload{Model: "b", Effort: "high"})
	revision := int64(0)
	op, err := e.app.Execute(context.Background(), owner, &protocol.Envelope{
		RequestID: e.ids.New(protocol.PrefixRequest), Operation: "conversation.set_settings", Target: protocol.Target{Type: "conversation"},
		ExpectedControlRevision: &revision, Payload: payload,
	}, app.SetConversationSettings{Available: true, Models: modelViews(h.Models)})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("set while processing = %+v, %v", op, err)
	}
	e.say("queued while first is running")
	starts := h.Starts()
	if len(starts) != 1 || starts[0].Model != "raw-a" || starts[0].Effort != "medium" || starts[0].ModelProvider != "provider-a" {
		t.Fatalf("running turn changed: %+v", starts)
	}
	once.Do(func() { close(release) })
	eventually(t, "both turns complete", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed'`) == 2
	})
	starts = h.Starts()
	if len(starts) != 2 || starts[1].Model != "raw-b" || starts[1].ModelProvider != "provider-b" || starts[1].Effort != "high" {
		t.Fatalf("next turn settings = %+v", starts)
	}
	if e.count(`SELECT COUNT(*) FROM main_turns WHERE model = 'raw-a' AND effort = 'medium' AND settings_revision = 0`) != 1 ||
		e.count(`SELECT COUNT(*) FROM main_turns WHERE model = 'raw-b' AND model_provider = 'provider-b' AND effort = 'high' AND settings_revision = 1`) != 1 {
		t.Fatal("turns did not preserve their actual model settings")
	}
	e.stop()
	e.start()
	e.say("after restart")
	eventually(t, "resumed thread uses persisted settings", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE status = 'completed' AND model = 'raw-b' AND settings_revision = 1`) == 2
	})
	if starts = h.Starts(); len(starts) != 3 || starts[2].Model != "raw-b" || starts[2].Effort != "high" {
		t.Fatalf("restarted model settings = %+v", starts)
	}
}
