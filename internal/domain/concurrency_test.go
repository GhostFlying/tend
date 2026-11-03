package domain

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestConcurrencyRequiresObservedSaturationAndDemand(t *testing.T) {
	start := int64(100000)
	s := ConcurrencyState{Group: protocol.ConcurrencyGroup{Min: 1, Initial: 4, Max: 8, Current: 4}}
	for i := 0; i <= 1800; i++ {
		running := int64(0)
		if i < 901 {
			running = 4
		}
		s.Sample(start+int64(i)*1000, 4, running, 1, true)
		if i == 10 {
			s.Successes = append(s.Successes, start+10000)
		}
		if i < 1800 && s.Increase(start+int64(i)*1000) {
			t.Fatal("increased before a full window")
		}
	}
	if s.Group.SaturatedMillis != 901000 {
		t.Fatalf("saturated=%d", s.Group.SaturatedMillis)
	}
	s.Group.Waiting = 0
	if s.Increase(start + 1800000) {
		t.Fatal("increased without demand")
	}
	s.Group.Waiting = 1
	if !s.Increase(start+1800000) || s.Group.Current != 5 {
		t.Fatalf("did not increase: %+v", s)
	}
	if s.Group.SaturatedMillis != 0 || s.Group.SuccessfulTurns != 0 {
		t.Fatal("old proof reused")
	}
}
func TestConcurrencyCongestionCoalescesAndMinimumStillCools(t *testing.T) {
	s := ConcurrencyState{Group: protocol.ConcurrencyGroup{Min: 1, Current: 3, Max: 8}}
	if !s.Congested(100000) || s.Group.Current != 2 {
		t.Fatal(s)
	}
	if s.Congested(101000) || s.Group.Current != 2 || s.Group.CooldownUntil != 131000 {
		t.Fatal(s)
	}
	if !s.Congested(131000) || s.Group.Current != 1 {
		t.Fatal(s)
	}
	if !s.Congested(161000) || s.Group.Current != 1 || s.Group.CooldownUntil != 191000 {
		t.Fatal(s)
	}
}
func TestConcurrencyMissingFeedbackInvalidatesProof(t *testing.T) {
	s := ConcurrencyState{Group: protocol.ConcurrencyGroup{Min: 1, Current: 2, Max: 4}}
	s.Sample(100000, 2, 2, 1, true)
	s.Sample(101000, 2, 2, 1, true)
	if s.Group.SaturatedMillis != 1000 {
		t.Fatal(s)
	}
	s.Sample(120000, 2, 2, 1, true)
	if s.Group.SaturatedMillis != 0 || s.Group.ObservationSince != 120000 {
		t.Fatal("gap counted as healthy", s)
	}
	s.Sample(121000, 2, 2, 1, false)
	if s.Group.FeedbackHealthy || s.Group.SaturatedMillis != 0 {
		t.Fatal(s)
	}
	s.Sample(122000, 2, 2, 1, true)
	if s.Group.SaturatedMillis != 0 {
		t.Fatal("unhealthy interval counted")
	}
	s.Group.Current = 1
	s.Sample(123000, 2, 2, 1, true)
	s.Sample(124000, 2, 2, 1, true)
	if s.Group.SaturatedMillis != 0 {
		t.Fatal("drain above budget counted")
	}
}
func TestConcurrencyPermanentQuotaOverrides429(t *testing.T) {
	for _, f := range []protocol.AgentFailure{{Code: "insufficient_quota", HTTPStatus: 429}, {Code: "unauthorized", HTTPStatus: 429}, {Code: "unknown", HTTPStatus: 503}} {
		if ConcurrencyCongestion(&f) {
			t.Fatal(f)
		}
	}
	for _, f := range []protocol.AgentFailure{{Code: "rate_limit_exceeded", WillRetry: true}, {Code: "server_overloaded"}, {Code: "httpConnectionFailed", HTTPStatus: 429}} {
		if !ConcurrencyCongestion(&f) {
			t.Fatal(f)
		}
	}
}
