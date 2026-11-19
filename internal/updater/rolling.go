package updater

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Remains independently readable if the owning supervisor state is damaged.
const rollingOwnershipMarker = "rolling-journals"

type RollingLaunch struct {
	Predecessor Identity                        `json:"predecessor"`
	Observation protocol.UpdaterSelfObservation `json:"observation"`
	Revision    int64                           `json:"revision"`
}

type DrainingProcess struct {
	Confirmed           bool     `json:"confirmed,omitempty"`
	WindowID            string   `json:"window_id,omitempty"`
	GrantBootID         string   `json:"grant_boot_id,omitempty"`
	GrantDeadlineMillis int64    `json:"grant_deadline_millis,omitempty"`
	AuthorizationID     string   `json:"authorization_id,omitempty"`
	StopSequence        int64    `json:"stop_sequence,omitempty"`
	StopRequested       bool     `json:"stop_requested,omitempty"`
	Stopped             bool     `json:"stopped,omitempty"`
	Process             Identity `json:"process"`
	JournalID           string   `json:"journal_id"`
	UpgradeID           string   `json:"upgrade_id"`
}

type RollingAuthority interface {
	AuthorizeLaunch(context.Context, Version, Binding, RollingLaunch) (Grant, error)
	Health(context.Context, Binding) (Health, error)
	Retire(context.Context, string, protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error)
}

// JournalSocket keeps the bridge in the same private directory. A bounded
// filename also fits Linux socket paths when launch IDs are long.
func JournalSocket(original, journal string) string {
	sum := sha256.Sum256([]byte(journal))
	return filepath.Join(filepath.Dir(original), fmt.Sprintf("r-%x.sock", sum[:12]))
}

// The configured runner journal denotes the original generation. Maintenance
// of a committed successor must snapshot and validate its actual writer.
func (s *Supervisor) serviceDataConfig() Config {
	return dataConfigForJournal(s.cfg, s.state.CurrentJournal)
}

func dataConfigForJournal(cfg Config, id string) Config {
	if cfg.Role != "runner" || id == "" || cfg.journalID == id {
		return cfg
	}
	cfg.journalID = id
	cfg.DataFiles = slices.Clone(cfg.DataFiles)
	for i, path := range cfg.DataFiles {
		name := filepath.Base(path)
		if name == "journal.db" || name == "journal.db.incarnation" {
			cfg.DataFiles[i] = filepath.Join(filepath.Dir(path), "generations", id, name)
		}
	}
	return cfg
}

type journalAuthority struct{ cfg Config }

func (a journalAuthority) bridge(journal string) (*HTTPAuthority, error) {
	cfg := a.cfg
	if cfg.AuthoritySocket == "" || journal == "" {
		return nil, errors.New("updater: independent runner bridge required")
	}
	cfg.AuthoritySocket = JournalSocket(cfg.AuthoritySocket, journal)
	return NewHTTPAuthority(cfg)
}
func (a journalAuthority) Health(ctx context.Context, b Binding) (Health, error) {
	bridge, err := a.bridge(b.ProcessInstanceID)
	if err != nil {
		return Health{}, err
	}
	defer bridge.client.CloseIdleConnections()
	return bridge.Health(ctx, b)
}
func (a journalAuthority) Retire(ctx context.Context, journal string, req protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error) {
	var result protocol.RunnerRetirementResult
	bridge, err := a.bridge(journal)
	if err != nil {
		return result, err
	}
	defer bridge.client.CloseIdleConnections()
	err = bridge.request(ctx, "http://localhost/updater/rolling-retire", req, &result)
	if err == nil && (result.UpgradeID != req.UpgradeID || result.Process != req.Process || result.Allowed && result.AuthorizationID == "" || req.AuthorizationID != "" && result.AuthorizationID != req.AuthorizationID) {
		err = errors.New("updater: retirement response identity changed")
	}
	return result, err
}

// Each retirement reconciles independently. A persistence error stops the
// entire pass before another local side effect can be attempted.
func (s *Supervisor) retireDrained(ctx context.Context) error {
	var failures []error
	for key, drain := range s.state.DrainingProcesses {
		if key == s.state.Process.LaunchID || drain.Process.LaunchID != key || s.state.CurrentJournal == "" && s.opts.WindowAuthority == nil {
			continue
		}
		if err := s.retirePredecessor(ctx, drain); err != nil {
			if persist := s.durabilityError(); persist != nil {
				return errors.Join(err, persist)
			}
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Supervisor) savePredecessor(drain DrainingProcess) error {
	if drain.Process == s.state.Process {
		if err := s.criticalState(); err != nil {
			return err
		}
		s.state.Format = max(s.state.Format, emergencyStateFormat)
		s.state.EmergencyPrimary = &drain
	} else {
		s.state.DrainingProcesses[drain.Process.LaunchID] = drain
	}
	return s.save()
}

func (s *Supervisor) retirementRPC(ctx context.Context, req protocol.RunnerRetirement) (protocol.RunnerRetirementResult, error) {
	if s.opts.WindowAuthority != nil && s.state.EmergencyPrimary != nil {
		return s.opts.WindowAuthority.Retire(ctx, req)
	}
	if s.opts.RollingAuthority == nil {
		return protocol.RunnerRetirementResult{}, errors.New("updater: retirement authority unavailable")
	}
	return s.opts.RollingAuthority.Retire(ctx, s.state.CurrentJournal, req)
}

// Natural retirement uses the original wire request. A server can offer a
// bounded, forced stop; only its affirmative echo can authorize that operation.
func (s *Supervisor) requestRetirement(ctx context.Context, req protocol.RunnerRetirement, drain *DrainingProcess) (bool, error) {
	result, err := s.retirementRPC(ctx, req)
	if err != nil {
		return false, err
	}
	if result.WindowID != "" && !result.Allowed {
		req.WindowID = result.WindowID
		boot, start, err := s.opts.BootClock()
		if err != nil {
			return false, err
		}
		result, err = s.retirementRPC(ctx, req)
		if err != nil {
			return false, err
		}
		// The runner may qualify for natural retirement between these two reads.
		if result.WindowID != "" || !result.Allowed {
			if result.WindowID != req.WindowID || result.AuthorizationID == "" {
				return false, nil
			}
			drain.WindowID = req.WindowID
			drain.GrantBootID = boot
			drain.GrantDeadlineMillis = start
			if result.Allowed && result.RemainingMillis > 0 && result.RemainingMillis <= 30*60*1000 {
				drain.GrantDeadlineMillis += result.RemainingMillis
			}
		}
	} else if !result.Allowed {
		return false, nil
	}
	drain.AuthorizationID = result.AuthorizationID
	drain.StopSequence = result.ReadySequence
	return true, s.savePredecessor(*drain)
}

func (s *Supervisor) stopGrantValid(drain DrainingProcess) (bool, error) {
	if drain.WindowID == "" {
		return true, nil
	}
	boot, now, err := s.opts.BootClock()
	return boot == drain.GrantBootID && now < drain.GrantDeadlineMillis, err
}

func (s *Supervisor) declineUnusedStop(ctx context.Context, req protocol.RunnerRetirement, drain DrainingProcess) error {
	if drain.StopRequested {
		return ErrUnknown
	}
	req.AuthorizationID, req.Unused = drain.AuthorizationID, true
	ack, err := s.retirementRPC(ctx, req)
	if err != nil {
		return err
	}
	if !ack.Unused || ack.Allowed || ack.AuthorizationID != drain.AuthorizationID {
		return ErrUnknown
	}
	drain.WindowID, drain.AuthorizationID, drain.GrantBootID = "", "", ""
	drain.GrantDeadlineMillis = 0
	if drain.Process == s.state.Process {
		s.state.EmergencyPrimary = nil
		return s.save()
	}
	return s.savePredecessor(drain)
}

func (s *Supervisor) retirePredecessor(ctx context.Context, drain DrainingProcess) error {
	req := protocol.RunnerRetirement{UpgradeID: drain.UpgradeID, Process: drain.Process}
	if drain.AuthorizationID == "" {
		authorized, err := s.requestRetirement(ctx, req, &drain)
		if err != nil || !authorized {
			return err
		}
	}
	req.WindowID = drain.WindowID
	if !drain.Stopped {
		got, err := s.opts.Process.Inspect(ctx, drain.Process.PID)
		if err == nil && got == drain.Process {
			if valid, e := s.stopGrantValid(drain); e != nil {
				return e
			} else if !valid {
				return s.declineUnusedStop(ctx, req, drain)
			}
			previouslyRequested := drain.StopRequested
			if previouslyRequested {
				// Repeating a signal requires a newer heartbeat of the same
				// exact process in addition to the original grant.
				observed, e := s.retirementRPC(ctx, req)
				if e != nil {
					return e
				}
				if !observed.Allowed || observed.AuthorizationID != drain.AuthorizationID || observed.ReadySequence <= drain.StopSequence {
					return ErrUnknown
				}
				drain.StopSequence = observed.ReadySequence
			}
			drain.StopRequested = true
			if err := s.savePredecessor(drain); err != nil {
				return err
			}
			timeout := time.Duration(s.cfg.StopTimeoutSeconds) * time.Second
			if drain.WindowID == "" {
				err = s.opts.Process.Stop(ctx, drain.Process, timeout)
			} else {
				guarded, ok := s.opts.Process.(interface {
					StopIf(context.Context, Identity, time.Duration, func() error) error
				})
				if !ok {
					return errors.New("updater: process driver cannot enforce a bounded stop")
				}
				expired := errors.New("stop expired before signal")
				err = guarded.StopIf(ctx, drain.Process, timeout, func() error {
					valid, err := s.stopGrantValid(drain)
					if err != nil {
						return err
					}
					if !valid {
						return expired
					}
					return ctx.Err()
				})
				if errors.Is(err, expired) {
					drain.StopRequested = previouslyRequested
					if err := s.savePredecessor(drain); err != nil {
						return err
					}
					return s.declineUnusedStop(ctx, req, drain)
				}
			}
			if err == nil {
				got, err = s.opts.Process.Inspect(ctx, drain.Process.PID)
			}
		}
		if !errors.Is(err, ErrNotRunning) && !(err == nil && got.StartTicks != 0 && got.StartTicks != drain.Process.StartTicks) {
			return errors.Join(ErrUnknown, err)
		}
		drain.Stopped = true
		if err := s.savePredecessor(drain); err != nil {
			return err
		}
	}
	req.AuthorizationID = drain.AuthorizationID
	result, err := s.retirementRPC(ctx, req)
	if err != nil || !result.Confirmed {
		return err
	}
	if drain.Process == s.state.Process {
		drain.Confirmed = true
		return s.savePredecessor(drain)
	}
	s.state.RetiredJournals = append(s.state.RetiredJournals, drain.JournalID)
	delete(s.state.DrainingProcesses, drain.Process.LaunchID)
	return s.save()
}
