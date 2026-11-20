package store

import (
	"slices"
	"time"
)

const (
	writerHistoryCapacity = 256
	writerWaitHolderLimit = 8
)

// WriterWaitTimeline summarizes observed ownership during one connection wait.
// Unattributed includes pool handoff, unobserved holders and lost history.
type WriterWaitTimeline struct {
	StartedAt        time.Time          `json:"started_at"`
	EndedAt          time.Time          `json:"ended_at"`
	HistoryFrom      time.Time          `json:"history_from"`
	HistoryTruncated bool               `json:"history_truncated"`
	MatchedHolders   int                `json:"matched_holders"`
	OmittedHolders   int                `json:"omitted_holders"`
	ObservedHeld     time.Duration      `json:"observed_held_ns"`
	LongestOverlap   time.Duration      `json:"longest_overlap_ns"`
	Unattributed     time.Duration      `json:"unattributed_ns"`
	Holders          []WriterWaitHolder `json:"holders,omitempty"`
}

// WriterWaitHolder records the observed interval of a write. If release has
// started, the pool handoff occurred between ReleaseStartedAt and ReleasedBy,
// whose upper bound can still be unknown. WaitHeld counts only confirmed
// ownership inside the wait, excluding the uncertain handoff interval.
type WriterWaitHolder struct {
	ID                uint64        `json:"write_id"`
	Source            WriteSource   `json:"source"`
	AcquiredAt        time.Time     `json:"acquired_at"`
	ReleaseStartedAt  time.Time     `json:"release_started_at,omitzero"`
	ReleasedBy        time.Time     `json:"released_by,omitzero"`
	WaitHeld          time.Duration `json:"wait_held_ns"`
	LastObservedStage string        `json:"last_observed_stage"`
	Begin             time.Duration `json:"begin_immediate_ns,omitempty"`
	Callback          time.Duration `json:"callback_inclusive_ns,omitempty"`
	Commit            time.Duration `json:"commit_ns,omitempty"`
	Rollback          time.Duration `json:"rollback_ns,omitempty"`
}

func (h *WriterWaitHolder) releasedBy(at time.Time) {
	if h.ReleasedBy.IsZero() || at.Before(h.ReleasedBy) {
		h.ReleasedBy = at
	}
}

func (d *writeDiagnostics) historyAtSlot(position uint64) *WriterWaitHolder {
	return &d.history[(position-1)%writerHistoryCapacity]
}

// waitTimeline is called under the diagnostics mutex. Both bounds are frozen
// by the waiting write, so concurrent activity after its wait is excluded.
func (d *writeDiagnostics) waitTimeline(start, end time.Time) *WriterWaitTimeline {
	if !end.After(start) {
		return nil
	}
	timeline := &WriterWaitTimeline{StartedAt: start, EndedAt: end, HistoryFrom: start}
	count := min(d.historyAt, writerHistoryCapacity)
	if d.historyAt > count {
		oldest := d.historyAtSlot(d.historyAt - count + 1).AcquiredAt
		if oldest.After(start) {
			timeline.HistoryTruncated = true
			timeline.HistoryFrom = earlierTime(oldest, end)
		}
	}

	// Visit newest first so equal overlaps prefer more recent holders.
	var selected [writerWaitHolderLimit]WriterWaitHolder
	if a := d.active; a.id != 0 {
		timeline.consider(&selected, WriterWaitHolder{
			ID: a.id, Source: a.source, AcquiredAt: a.acquired, LastObservedStage: a.stage,
		})
	}
	for i := range count {
		timeline.consider(&selected, *d.historyAtSlot(d.historyAt - i))
	}
	n := min(timeline.MatchedHolders, writerWaitHolderLimit)
	timeline.Holders = slices.Clone(selected[:n])
	timeline.OmittedHolders = timeline.MatchedHolders - n
	timeline.Unattributed = end.Sub(start) - timeline.ObservedHeld
	slices.SortStableFunc(timeline.Holders, func(a, b WriterWaitHolder) int {
		return a.AcquiredAt.Compare(b.AcquiredAt)
	})
	return timeline
}

func (t *WriterWaitTimeline) consider(selected *[writerWaitHolderLimit]WriterWaitHolder, h WriterWaitHolder) {
	if !h.AcquiredAt.Before(t.EndedAt) || (!h.ReleasedBy.IsZero() && !h.ReleasedBy.After(t.StartedAt)) {
		return
	}
	from, until := t.StartedAt, t.EndedAt
	if h.AcquiredAt.After(from) {
		from = h.AcquiredAt
	}
	if !h.ReleaseStartedAt.IsZero() {
		until = earlierTime(until, h.ReleaseStartedAt)
	}
	h.WaitHeld = max(0, until.Sub(from))
	t.ObservedHeld += h.WaitHeld
	t.LongestOverlap = max(t.LongestOverlap, h.WaitHeld)

	// Keep the greatest confirmed contributions without allocating a copy of
	// the ring, but count every match even when the log omits its details.
	n := min(t.MatchedHolders, len(selected))
	t.MatchedHolders++
	for i := range n {
		if h.WaitHeld > selected[i].WaitHeld {
			copy(selected[i+1:], selected[i:])
			selected[i] = h
			return
		}
	}
	if n < len(selected) {
		selected[n] = h
	}
}

func earlierTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
