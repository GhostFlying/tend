package domain

import (
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

const (
	ConcurrencySamplePeriod = 30 * time.Second
	ConcurrencyWindow       = 30 * time.Minute
	ConcurrencySaturation   = 15 * time.Minute
	ConcurrencyCooldown     = 30 * time.Second
)

type ConcurrencyInterval struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// ConcurrencyState is a deterministic controller; all time and load are inputs.
type ConcurrencyState struct {
	Group        protocol.ConcurrencyGroup `json:"group"`
	LastSample   int64                     `json:"last_sample"`
	LastRunning  int64                     `json:"last_running"`
	LastHealthy  bool                      `json:"last_healthy"`
	LastDecrease int64                     `json:"last_decrease"`
	LastChangeID int64                     `json:"last_change_id"`
	Successes    []int64                   `json:"successes"`
	Intervals    []ConcurrencyInterval     `json:"intervals"`
}

func (s *ConcurrencyState) Reset(now int64) {
	s.Group.ObservationSince = now
	s.Group.SaturatedMillis, s.Group.SuccessfulTurns = 0, 0
	s.Intervals, s.Successes = nil, nil
	s.LastSample = now
}

// Sample integrates confirmed load; a missing controller tick invalidates proof.
func (s *ConcurrencyState) Sample(now, occupied, running, waiting int64, healthy bool) {
	g := &s.Group
	if s.LastSample == 0 || now < s.LastSample || now-s.LastSample > (2*ConcurrencySamplePeriod).Milliseconds() || !healthy || !s.LastHealthy {
		s.Reset(max(now, g.CooldownUntil))
		s.LastSample = now
	} else if s.LastRunning == g.Current && s.LastSample >= g.CooldownUntil && now > s.LastSample {
		n := len(s.Intervals)
		if n > 0 && s.Intervals[n-1].End == s.LastSample {
			s.Intervals[n-1].End = now
		} else {
			s.Intervals = append(s.Intervals, ConcurrencyInterval{Start: s.LastSample, End: now})
		}
	}
	s.LastSample, s.LastRunning, s.LastHealthy = now, running, healthy
	g.Occupied, g.Running, g.Waiting, g.FeedbackHealthy = occupied, running, waiting, healthy
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
	g.CooldownUntil = now + ConcurrencyCooldown.Milliseconds()
	s.Reset(g.CooldownUntil)
	if s.LastDecrease != 0 && now-s.LastDecrease < ConcurrencyCooldown.Milliseconds() {
		return false
	}
	g.Current = max(g.Min, g.Current-1)
	s.LastDecrease = now
	return true
}

func (s *ConcurrencyState) Increase(now int64) bool {
	g := &s.Group
	if !g.FeedbackHealthy || g.Current >= g.Max || now < g.CooldownUntil || now-g.ObservationSince < ConcurrencyWindow.Milliseconds() || g.SaturatedMillis < ConcurrencySaturation.Milliseconds() || g.SuccessfulTurns == 0 || g.Waiting == 0 {
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
