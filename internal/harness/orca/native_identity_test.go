package orca

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNativeIdentityReportsActualLaunchSessionAndTakeoverWithoutSendingInput(t *testing.T) {
	f := newWorkerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startup := make(chan protocol.NativeIdentity, 8)
	spec := f.spec(protocol.RoleImplement)
	spec.RoleInstructions = "PRIVATE_ROLE_CONFIG"
	spec.OnStartupEvent = func(_ context.Context, event harness.WorkerEvent) error {
		if event.Kind == protocol.EventNativeIdentity {
			var identity protocol.NativeIdentity
			if err := json.Unmarshal(event.Body, &identity); err != nil {
				return err
			}
			startup <- identity
		}
		return nil
	}
	w, err := f.ws.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); w.Wait() }()
	first := <-startup
	if first.State != "partial" || first.Terminal != "term_1" || first.Pane != "tab:1" || first.ProviderID != f.orca.wt || first.SessionID != "" {
		t.Fatal(first)
	}
	f.orca.mu.Lock()
	f.orca.agents["tab:1"].ProviderSession = &ProviderSession{ID: "actual-session"}
	f.orca.mu.Unlock()
	await := func(state string) protocol.NativeIdentity {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case event, ok := <-w.Events():
				if !ok {
					t.Fatal("worker ended before identity")
				}
				if event.Kind != protocol.EventNativeIdentity {
					continue
				}
				if strings.Contains(string(event.Body), "PRIVATE_ROLE_CONFIG") {
					t.Fatal("private configuration uploaded")
				}
				var identity protocol.NativeIdentity
				if err := json.Unmarshal(event.Body, &identity); err != nil {
					t.Fatal(err)
				}
				if identity.State == state {
					return identity
				}
			case <-timer.C:
				t.Fatal("identity was not reported", state)
			}
		}
	}
	known := await("known")
	if known.SessionID != "actual-session" || known.ControllerObservation != "unknown" || f.orca.count("terminal send", nil) != 0 || f.orca.count("agent launch", nil) != 1 {
		t.Fatal("identity inspection changed native work", known)
	}
	f.orca.mu.Lock()
	f.orca.agents["tab:1"].ProviderSession = &ProviderSession{ID: "owner-replacement-session"}
	f.orca.mu.Unlock()
	changed := await("unknown")
	if changed.SessionID != "actual-session" || changed.ObservedSessionID != "owner-replacement-session" || changed.ControllerObservation != "takeover_observed" {
		t.Fatal("foreign session silently adopted", changed)
	}
}
