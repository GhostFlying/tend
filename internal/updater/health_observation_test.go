//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHealthObservationSeparatesProcessAndAuthorityFailureAcrossReopen(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	original := h.s.Status().Operation.Binding
	h.a.failHealth = true
	if err := h.s.Step(t.Context()); err == nil {
		t.Fatal("unavailable authority committed candidate")
	}
	op := h.s.Status().Operation
	if op.LastHealth == nil || op.LastHealth.ProcessAlive == nil || !*op.LastHealth.ProcessAlive || op.LastHealth.ServerConfirmed != nil || !slices.Contains(op.LastHealth.Failures, "authority_read_failed") || op.LastHealth.FailureCause == "" {
		t.Fatal("healthy process hid missing authority", op.LastHealth)
	}
	h.reopen(t)
	op = h.s.Status().Operation
	if op.Binding != original || op.LastHealth == nil || !slices.Contains(op.LastHealth.Failures, "authority_read_failed") {
		t.Fatal("restart erased original observation", op)
	}
	h.a.failHealth = false
	id := h.p.live[op.CandidateProcess.PID]
	id.LaunchID = "unrelated"
	h.p.live[id.PID] = id
	if err := h.s.Step(t.Context()); err == nil {
		t.Fatal("wrong native process identity was accepted")
	}
	op = h.s.Status().Operation
	if op.Phase == "committed" || op.LastHealth.ProcessAlive == nil || !*op.LastHealth.ProcessAlive || op.LastHealth.ProcessIdentityMatched == nil || *op.LastHealth.ProcessIdentityMatched || op.LastHealth.ServerConfirmed == nil || !*op.LastHealth.ServerConfirmed || !slices.Contains(op.LastHealth.Failures, "candidate_process_identity_mismatch") {
		t.Fatal("server confirmation hid wrong process identity", op.LastHealth)
	}
}

func TestUpgradePhaseTimingsRetainOriginalObservedBoundaries(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.now = h.now.Add(3 * time.Second)
	h.step(t)
	op := h.s.Status().Operation
	if len(op.PhaseTimings) < 2 || op.PhaseTimings[0].ElapsedMillis == nil || *op.PhaseTimings[0].ElapsedMillis != 3000 {
		t.Fatal("phase timing omitted original transition", op.PhaseTimings)
	}
	before := op.PhaseTimings[0]
	h.reopen(t)
	after := h.s.Status().Operation.PhaseTimings[0]
	if after.EnteredAt != before.EnteredAt || after.ExitedAt != before.ExitedAt || after.ElapsedMillis == nil || *after.ElapsedMillis != *before.ElapsedMillis {
		t.Fatal("reopen replaced original phase timing", before, after)
	}
	// A legacy current phase has no known original entry timestamp.
	h.s.state.Operation.PhaseTimings = nil
	h.s.durable.Operation.PhaseTimings = nil
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	if !h.s.Status().Operation.PhaseTimings[0].EnteredAt.IsZero() {
		t.Fatal("legacy phase start was invented")
	}
}

func TestHTTPHealthDiagnosticsOptInAcceptsOldAndNewAuthorities(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "old", true: "new"}[modern], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("diagnostics") != "1" {
					t.Error("new caller did not opt in")
				}
				var binding Binding
				if json.NewDecoder(r.Body).Decode(&binding) != nil {
					t.Error("invalid request")
					return
				}
				health := Health{Binding: binding, ServerConfirmed: true, ServerRevision: 1, Healthy: true}
				if modern {
					ready := true
					drained := false
					health.Diagnostics = &protocol.UpgradeHealthDiagnostics{ServiceReady: &ready, MainDrained: &drained}
				}
				_ = json.NewEncoder(w).Encode(health)
			}))
			defer server.Close()
			authority, err := NewHTTPAuthority(Config{AuthorizeURL: server.URL + "/authorize", HealthURL: server.URL + "/health?existing=value"})
			if err != nil {
				t.Fatal(err)
			}
			health, err := authority.Health(context.Background(), Binding{UpgradeID: "original"})
			if err != nil || !health.Healthy || (health.Diagnostics != nil) != modern {
				t.Fatal("mixed authority diagnostics failed", health, err)
			}
		})
	}
}

func TestHealthDiagnosticFailurePreservesOriginalErrorCause(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	cause := errors.New("original authority failure")
	h.s.opts.Authority = deadlineAuthority{h.a, func(context.Context, Binding) (Health, error) { return Health{}, cause }}
	if err := h.s.Step(t.Context()); !errors.Is(err, cause) {
		t.Fatal("observation replaced original cause", err)
	}
}
