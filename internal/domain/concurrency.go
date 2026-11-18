package domain

import (
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

const (
	ConcurrencySamplePeriod = 30 * time.Second
	ConcurrencyWindow       = 15 * time.Minute
	ConcurrencySaturation   = 8 * time.Minute
	ConcurrencyCooldown     = 30 * time.Second
)

type ConcurrencyHealth int

const (
	ConcurrencyHealthy ConcurrencyHealth = iota
	ConcurrencyBindingPending
	ConcurrencyUnhealthy
)

type ConcurrencyInterval struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// ConcurrencyState is a deterministic controller; all time and load are inputs.
type ConcurrencyState struct {
	Group              protocol.ConcurrencyGroup `json:"group"`
	LastSample         int64                     `json:"last_sample"`
	LastRunning        int64                     `json:"last_running"`
	LastHealthy        bool                      `json:"last_healthy"`
	LastBindingPending bool                      `json:"last_binding_pending,omitempty"`
	// ObservationReadyAt excludes unconfirmed time from the healthy duration.
	// The saturation intervals still expire against the rolling window.
	ObservationReadyAt int64                 `json:"observation_ready_at,omitempty"`
	LastDecrease       int64                 `json:"last_decrease"`
	LastChangeID       int64                 `json:"last_change_id"`
	Successes          []int64               `json:"successes"`
	Intervals          []ConcurrencyInterval `json:"intervals"`
}

func (s *ConcurrencyState) Reset(now int64) {
	now = max(now, s.LastSample)
	s.Group.ObservationSince = max(now, s.Group.CooldownUntil)
	s.ObservationReadyAt = 0
	s.Group.SaturatedMillis, s.Group.SuccessfulTurns = 0, 0
	s.Intervals, s.Successes = nil, nil
	s.LastSample = now
}

// Sample integrates confirmed load; a missing controller tick invalidates proof.
func (s *ConcurrencyState) Sample(now, occupied, running, waiting int64, health ConcurrencyHealth) {
	// Facts are applied in transaction order. An older clock reading can
	// change the current load at the cursor without counting any time twice.
	now = max(now, s.LastSample)
	g := &s.Group
	healthy := health == ConcurrencyHealthy
	if s.LastSample == 0 || now-s.LastSample > (2*ConcurrencySamplePeriod).Milliseconds() || health == ConcurrencyUnhealthy || !s.LastHealthy && !s.LastBindingPending {
		s.Reset(now)
	} else if !healthy || !s.LastHealthy {
		if elapsed := now - max(s.LastSample, g.CooldownUntil); elapsed > 0 {
			if s.ObservationReadyAt == 0 {
				s.ObservationReadyAt = g.ObservationSince + ConcurrencyWindow.Milliseconds()
			}
			s.ObservationReadyAt += elapsed
		}
	} else if s.LastRunning == g.Current && s.LastSample >= g.CooldownUntil && now > s.LastSample {
		n := len(s.Intervals)
		if n > 0 && s.Intervals[n-1].End == s.LastSample {
			s.Intervals[n-1].End = now
		} else {
			s.Intervals = append(s.Intervals, ConcurrencyInterval{Start: s.LastSample, End: now})
		}
	}
	s.LastSample, s.LastRunning, s.LastHealthy = now, running, healthy
	s.LastBindingPending = health == ConcurrencyBindingPending
	g.Occupied, g.Running, g.Waiting, g.FeedbackHealthy = occupied, running, waiting, healthy
	s.RefreshWindow(now)
}

// RefreshWindow projects only already observed evidence at the read clock.
// It never integrates time beyond the last confirmed sample.
func (s *ConcurrencyState) RefreshWindow(now int64) {
	g := &s.Group
	cutoff := max(g.ObservationSince, now-ConcurrencyWindow.Milliseconds())
	g.ObservationSince = cutoff
	kept := s.Intervals[:0]
	g.SaturatedMillis = 0
	for _, v := range s.Intervals {
		if v.End > cutoff {
			v.Start = max(v.Start, cutoff)
			kept = append(kept, v)
			g.SaturatedMillis += v.End - v.Start
		}
	}
	s.Intervals = kept
	successes := s.Successes[:0]
	for _, at := range s.Successes {
		if at >= cutoff {
			successes = append(successes, at)
		}
	}
	s.Successes = successes
	g.SuccessfulTurns = int64(len(successes))
}

func (s *ConcurrencyState) Congested(now int64) bool {
	g := &s.Group
	now = max(now, s.LastSample)
	g.CooldownUntil = now + ConcurrencyCooldown.Milliseconds()
	s.Reset(now)
	if s.LastDecrease != 0 && now-s.LastDecrease < ConcurrencyCooldown.Milliseconds() {
		return false
	}
	g.Current = max(g.Min, g.Current-1)
	s.LastDecrease = now
	return true
}

func (s *ConcurrencyState) Increase(now int64) bool {
	g := &s.Group
	if !g.FeedbackHealthy || g.Current >= g.Max || now < g.CooldownUntil || now < s.ObservationReadyAt || now-g.ObservationSince < ConcurrencyWindow.Milliseconds() || g.SaturatedMillis < ConcurrencySaturation.Milliseconds() || g.SuccessfulTurns == 0 || g.Waiting == 0 {
		return false
	}
	g.Current++
	s.Reset(now)
	return true
}

// ConcurrencyCongestion deliberately excludes permanent quota and auth failures.
func ConcurrencyCongestion(f *protocol.AgentFailure) bool {
	if f == nil {
		return false
	}
	switch f.Code {
	case "insufficient_quota", "quota_exceeded", "organization_usage_limit_exceeded", "unauthorized", "authentication_error", "billing_hard_limit_reached", "insufficientQuota", "quotaExceeded", "usageLimitExceeded", "usage_limit_exceeded", "billingHardLimitReached", "authenticationError":
		return false
	case "rate_limit_exceeded", "rateLimitExceeded", "server_overloaded", "server_is_overloaded", "overloaded", "slow_down":
		return true
	}
	return f.HTTPStatus == 429
}
