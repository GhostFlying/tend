package updater

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// HealthWindow retains the first budget independently of later observations.
// A late positive observation never erases a previously elapsed deadline.
type HealthWindow struct {
	BootID           string `json:"boot_id"`
	DeadlineMillis   int64  `json:"deadline_millis"`
	DeadlineMono     int64  `json:"deadline_mono"`
	TimedOutAtMillis int64  `json:"timed_out_at_millis,omitempty"`
	Cause            string `json:"cause,omitempty"`
}

func boundedStateFormat(format int) bool { return format == 4 || format == 5 }

func (s *Supervisor) criticalState() error {
	if s.state.UpdaterEpoch == "" {
		epoch, err := s.opts.NewID()
		if err != nil {
			return err
		}
		boot, _, err := s.opts.BootClock()
		if err != nil {
			return err
		}
		s.state.UpdaterEpoch, s.state.UpdaterBootID = epoch, boot
	}
	// Prior writers ignore optional JSON fields. They must refuse this format.
	s.state.Format = 5
	return nil
}

func (s *Supervisor) ensureHealthWindow(op *Operation) error {
	if op.HealthWindow != nil {
		return nil
	}
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return err
	}
	now := s.opts.Now()
	deadline := op.HealthStartedAt.Add(time.Duration(s.cfg.HealthTimeoutSeconds) * time.Second)
	remaining := deadline.Sub(now).Milliseconds()
	if op.HealthStartedAt.IsZero() || now.Before(op.HealthStartedAt) {
		// A legacy record without a usable anchor cannot receive a new budget.
		remaining = 0
		deadline = now
	}
	if err := s.criticalState(); err != nil {
		return err
	}
	op.HealthWindow = &HealthWindow{BootID: boot, DeadlineMillis: deadline.UnixMilli(), DeadlineMono: mono + max(0, remaining)}
	return s.save()
}

func (s *Supervisor) healthRemaining(op *Operation) (time.Duration, error) {
	boot, mono, err := s.opts.BootClock()
	if err != nil {
		return 0, err
	}
	w := op.HealthWindow
	if w == nil {
		return 0, errors.New("updater: fixed health window missing")
	}
	if boot != w.BootID {
		return 0, nil
	}
	return time.Duration(min(w.DeadlineMillis-s.opts.Now().UnixMilli(), w.DeadlineMono-mono)) * time.Millisecond, nil
}

func (s *Supervisor) healthReadContext(ctx context.Context, op *Operation) (context.Context, context.CancelFunc, error) {
	remaining, err := s.healthRemaining(op)
	if err != nil {
		return nil, nil, err
	}
	budget := min(remaining, 10*time.Second)
	if budget <= 0 {
		// Late reads may only confirm the already selected process. They cannot
		// extend stop/start authority or repeat any external side effect.
		budget = 2 * time.Second
	}
	child, cancel := context.WithTimeout(ctx, budget)
	return child, cancel, nil
}

func (s *Supervisor) finishHealthRead(op *Operation, cause error) error {
	remaining, err := s.healthRemaining(op)
	if err != nil {
		return errors.Join(cause, err)
	}
	if remaining > 0 {
		return cause
	}
	if op.HealthWindow.TimedOutAtMillis == 0 {
		op.HealthWindow.TimedOutAtMillis = s.opts.Now().UnixMilli()
		op.HealthWindow.Cause = "candidate health remains unconfirmed"
		if cause != nil {
			op.HealthWindow.Cause = cause.Error()
		}
		if err := s.save(); err != nil {
			return errors.Join(cause, err)
		}
	}
	if op.Phase == "committed" {
		return cause
	}
	if cause == nil {
		cause = errors.New("candidate health remains unconfirmed")
	}
	return fmt.Errorf("updater: health deadline exceeded; original candidate requires reconciliation or independently authorized replacement: %w", cause)
}
