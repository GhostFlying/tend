package app

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func settingsCommand() SetConversationSettings {
	return SetConversationSettings{Available: true, Models: []protocol.ConversationModel{
		{ID: "model-a", DefaultEffort: "medium", SupportedEfforts: []string{"low", "medium", "high"}},
		{ID: "fixed", DefaultEffort: "high", SupportedEfforts: []string{}},
	}}
}

func TestConversationSettingsDeduplicateAndRejectStale(t *testing.T) {
	h := newHarness(t)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	env := h.env("conversation.set_settings", protocol.Target{Type: "conversation"},
		protocol.SetConversationSettingsPayload{Model: "model-a", Effort: "high"}, rev(0))
	first := h.exec(owner, env, settingsCommand())
	if first.Status != protocol.OpSucceeded {
		t.Fatalf("first = %+v", first)
	}
	// Reconciliation must still return the receipt if the catalog went away.
	replay := h.exec(owner, env, SetConversationSettings{})
	if string(first.Result) != string(replay.Result) || replay.Status != first.Status {
		t.Fatalf("replay = %+v, want %+v", replay, first)
	}
	stale := h.exec(owner, h.env(env.Operation, env.Target, protocol.SetConversationSettingsPayload{Model: "model-a", Effort: "low"}, rev(0)), settingsCommand())
	if stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("stale = %+v", stale)
	}
	got, err := h.app.ConversationSettings(context.Background())
	if err != nil || got.Model != "model-a" || got.Effort != "high" || got.Revision != 1 {
		t.Fatalf("settings = %+v, %v", got, err)
	}
	if count := h.count("events"); count != 1 {
		t.Fatalf("%d events, want one settings change", count)
	}
}

func TestConversationSettingsValidateCapabilitiesAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, model, effort string
		kind                protocol.PrincipalKind
		code                protocol.Code
	}{
		{"unknown model", "missing", "high", protocol.PrincipalOwner, protocol.CodeInvalidRequest},
		{"unsupported effort", "model-a", "ultra", protocol.PrincipalOwner, protocol.CodeInvalidRequest},
		{"fixed effort", "fixed", "low", protocol.PrincipalOwner, protocol.CodeInvalidRequest},
		{"fixed default", "fixed", "high", protocol.PrincipalOwner, ""},
		{"agent cannot change", "model-a", "high", protocol.PrincipalMainAgent, protocol.CodeForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			op, err := h.app.Execute(context.Background(), protocol.Principal{Kind: tc.kind, Subject: "caller", Client: "test", Thread: "thread"}, h.env("conversation.set_settings", protocol.Target{Type: "conversation"},
				protocol.SetConversationSettingsPayload{Model: tc.model, Effort: tc.effort}, rev(0)), settingsCommand())
			if tc.code == protocol.CodeForbidden {
				if protocol.CodeOf(err) != tc.code {
					t.Fatalf("authorization = %v, want %s", err, tc.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.code == "" {
				if op.Status != protocol.OpSucceeded {
					t.Fatalf("result = %+v", op)
				}
			} else if op.Error == nil || op.Error.Code != tc.code {
				t.Fatalf("result = %+v, want %s", op, tc.code)
			}
		})
	}
}

func TestConcurrentConversationSettingsHaveOneWinner(t *testing.T) {
	h := newHarness(t)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	requests := []*protocol.Envelope{}
	for _, effort := range []string{"low", "high"} {
		requests = append(requests, h.env("conversation.set_settings", protocol.Target{Type: "conversation"},
			protocol.SetConversationSettingsPayload{Model: "model-a", Effort: effort}, rev(0)))
	}
	results := make(chan protocol.Operation, 2)
	var wg sync.WaitGroup
	for _, request := range requests {
		wg.Go(func() {
			op, err := h.app.Execute(context.Background(), owner, request, settingsCommand())
			if err != nil {
				t.Errorf("execute: %v", err)
			}
			results <- op
		})
	}
	wg.Wait()
	close(results)
	winners, stale := 0, 0
	for op := range results {
		if op.Status == protocol.OpSucceeded {
			winners++
		} else if op.Error != nil && op.Error.Code == protocol.CodeStaleRevision {
			stale++
		}
	}
	if winners != 1 || stale != 1 {
		t.Fatalf("%d winners, %d stale; want one each", winners, stale)
	}
}

func TestConversationSettingsSurviveStoreRestart(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := store.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			_ = s.Close()
		}
	}()
	ids := testkit.NewIDGen(clock, 8)
	a := New(s, ids, clock.Now)
	env := &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "conversation.set_settings",
		Target: protocol.Target{Type: "conversation"}, ExpectedControlRevision: rev(0),
		Payload: []byte(`{"model":"model-a","effort":"high"}`)}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, env, settingsCommand())
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("set = %+v, %v", op, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := New(s, testkit.NewIDGen(clock, 9), clock.Now).ConversationSettings(ctx)
	if err != nil || got.Model != "model-a" || got.Effort != "high" || got.Revision != 1 {
		t.Fatalf("restarted settings = %+v, %v", got, err)
	}
}
