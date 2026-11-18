package domain

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestConcurrencyIncreaseAtObservationBoundaries(t *testing.T) {
	const (
		window       = 15 * time.Minute
		fullLoad     = 8 * time.Minute
		samplePeriod = 30 * time.Second
	)
	for _, tc := range []struct {
		name         string
		observed     time.Duration
		saturated    time.Duration
		wantIncrease bool
	}{
		{"healthy window one millisecond short", window - time.Millisecond, fullLoad, false},
		{"full load one millisecond short", window, fullLoad - time.Millisecond, false},
		{"both thresholds met exactly", window, fullLoad, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const start int64 = 100000
			s := ConcurrencyState{Group: protocol.ConcurrencyGroup{Min: 1, Initial: 4, Max: 8, Current: 4}}
			s.Sample(start, 4, 4, 1, ConcurrencyHealthy)
			sampleUntil := func(end time.Duration, nextRunning int64) {
				at := start + end.Milliseconds()
				for s.LastSample+samplePeriod.Milliseconds() < at {
					s.Sample(s.LastSample+samplePeriod.Milliseconds(), 4, s.LastRunning, 1, ConcurrencyHealthy)
				}
				s.Sample(at, 4, nextRunning, 1, ConcurrencyHealthy)
			}

			// Occupancy stays at the cap throughout. Only the two intervals of
			// confirmed running work count toward the saturation requirement.
			sampleUntil(4*time.Minute, 0)
			s.Successes = append(s.Successes, s.LastSample)
			sampleUntil(6*time.Minute, 4)
			sampleUntil(2*time.Minute+tc.saturated, 0)
			sampleUntil(tc.observed, 0)
			if len(s.Intervals) != 2 || s.Group.SaturatedMillis != tc.saturated.Milliseconds() || s.Group.ObservationSince != start || s.Group.SuccessfulTurns != 1 {
				t.Fatalf("unexpected observation evidence: %+v", s)
			}

			now := start + tc.observed.Milliseconds()
			if got := s.Increase(now); got != tc.wantIncrease {
				t.Fatalf("Increase() = %v, want %v", got, tc.wantIncrease)
			}
			if !tc.wantIncrease {
				if s.Group.Current != 4 {
					t.Fatalf("current = %d after rejected increase, want 4", s.Group.Current)
				}
				return
			}
			if s.Group.Current != 5 || s.Group.ObservationSince != now || s.Group.SaturatedMillis != 0 || s.Group.SuccessfulTurns != 0 || len(s.Intervals) != 0 || len(s.Successes) != 0 {
				t.Fatalf("increase did not add one and reset the observation: %+v", s)
			}
			if s.Increase(now) {
				t.Fatal("increased again using the previous observation")
			}
		})
	}
}

func TestConcurrencyIncreaseRequiresOtherPromotionConditions(t *testing.T) {
	const now = int64(1000000)
	ready := protocol.ConcurrencyGroup{
		Current:          4,
		Max:              8,
		ObservationSince: now - (15 * time.Minute).Milliseconds(),
		SaturatedMillis:  (8 * time.Minute).Milliseconds(),
		SuccessfulTurns:  1,
		Waiting:          1,
		FeedbackHealthy:  true,
		CooldownUntil:    now,
	}
	for _, tc := range []struct {
		name   string
		change func(*protocol.ConcurrencyGroup)
	}{
		{"no successful turn", func(g *protocol.ConcurrencyGroup) { g.SuccessfulTurns = 0 }},
		{"no claimable demand", func(g *protocol.ConcurrencyGroup) { g.Waiting = 0 }},
		{"unhealthy feedback", func(g *protocol.ConcurrencyGroup) { g.FeedbackHealthy = false }},
		{"at the cap", func(g *protocol.ConcurrencyGroup) { g.Max = g.Current }},
		{"cooldown one millisecond short", func(g *protocol.ConcurrencyGroup) { g.CooldownUntil++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ConcurrencyState{Group: ready}
			tc.change(&s.Group)
			if s.Increase(now) || s.Group.Current != ready.Current {
				t.Fatalf("increased with %s: %+v", tc.name, s)
			}
		})
	}

	s := ConcurrencyState{Group: ready}
	if !s.Increase(now) || s.Group.Current != ready.Current+1 {
		t.Fatalf("did not increase when the cooldown expired: %+v", s)
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
	s.Sample(100000, 2, 2, 1, ConcurrencyHealthy)
	s.Sample(101000, 2, 2, 1, ConcurrencyHealthy)
	if s.Group.SaturatedMillis != 1000 {
		t.Fatal(s)
	}
	s.Sample(162000, 2, 2, 1, ConcurrencyHealthy)
	if s.Group.SaturatedMillis != 0 || s.Group.ObservationSince != 162000 {
		t.Fatal("gap counted as healthy", s)
	}
	s.Sample(163000, 2, 2, 1, ConcurrencyUnhealthy)
	if s.Group.FeedbackHealthy || s.Group.SaturatedMillis != 0 {
		t.Fatal(s)
	}
	s.Sample(164000, 2, 2, 1, ConcurrencyHealthy)
	if s.Group.SaturatedMillis != 0 {
		t.Fatal("unhealthy interval counted")
	}
	s.Group.Current = 1
	s.Sample(165000, 2, 2, 1, ConcurrencyHealthy)
	s.Sample(166000, 2, 2, 1, ConcurrencyHealthy)
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

func TestConcurrencyThirtySecondSamplingKeepsOnlyObservedProof(t *testing.T) {
	s := ConcurrencyState{Group: protocol.ConcurrencyGroup{Min: 1, Current: 2, Max: 4}}
	s.Sample(100000, 2, 2, 1, ConcurrencyHealthy)
	s.Sample(130000, 2, 2, 1, ConcurrencyHealthy)
	if s.Group.SaturatedMillis != 30000 {
		t.Fatal("normal sample lost proof", s)
	}
	s.Sample(190001, 2, 2, 1, ConcurrencyHealthy)
	if s.Group.SaturatedMillis != 0 {
		t.Fatal("missed samples fabricated proof", s)
	}
}

func TestConcurrencyLateSamplesApplyLoadWithoutReplayingTime(t *testing.T) {
	const start int64 = 100000
	s := ConcurrencyState{Group: protocol.ConcurrencyGroup{Current: 2, Max: 4}}
	s.Sample(start, 2, 2, 1, ConcurrencyHealthy)
	s.Sample(start+30000, 2, 2, 1, ConcurrencyHealthy)

	// Apply a load transition whose timestamp was taken before the latest
	// committed sample. That sample's confirmed load remains accounted for.
	s.Sample(start+29257, 2, 1, 1, ConcurrencyHealthy)
	if s.LastSample != start+30000 || s.Group.ObservationSince != start || s.Group.SaturatedMillis != 30000 || s.Group.Running != 1 {
		t.Fatalf("late transition rewrote evidence or dropped the new load: %+v", s)
	}
	s.Sample(start+29257, 2, 1, 1, ConcurrencyHealthy)
	s.Sample(start+60000, 2, 2, 1, ConcurrencyHealthy)
	s.Sample(start+90000, 2, 2, 1, ConcurrencyHealthy)
	if s.Group.SaturatedMillis != 60000 || len(s.Intervals) != 2 {
		t.Fatalf("late or repeated sample counted twice: %+v", s)
	}

	s.Sample(start+89999, 2, 0, 1, ConcurrencyUnhealthy)
	if s.LastSample != start+90000 || s.Group.ObservationSince != start+90000 || s.Group.SaturatedMillis != 0 || s.Group.FeedbackHealthy {
		t.Fatalf("late confirmed feedback loss must still invalidate the observation: %+v", s)
	}
}

func TestConcurrencyPendingBindingCannotBridgeSamplingGap(t *testing.T) {
	const start int64 = 100000
	s := ConcurrencyState{Group: protocol.ConcurrencyGroup{Current: 2, Max: 4}}
	s.Sample(start, 2, 2, 1, ConcurrencyHealthy)
	s.Sample(start+30000, 2, 2, 1, ConcurrencyHealthy)
	s.Sample(start+30000, 2, 1, 1, ConcurrencyBindingPending)
	if s.Group.ObservationSince != start || s.Group.SaturatedMillis != 30000 || s.Group.FeedbackHealthy {
		t.Fatalf("pending binding should keep existing evidence: %+v", s)
	}

	s.Sample(start+90001, 2, 1, 1, ConcurrencyBindingPending)
	if s.Group.ObservationSince != start+90001 || s.Group.SaturatedMillis != 0 || s.Group.FeedbackHealthy {
		t.Fatalf("pending binding masked an actual sampling gap: %+v", s)
	}
}
