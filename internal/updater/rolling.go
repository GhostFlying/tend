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

type RollingLaunch struct {
	Predecessor Identity                        `json:"predecessor"`
	Observation protocol.UpdaterSelfObservation `json:"observation"`
	Revision    int64                           `json:"revision"`
}

type DrainingProcess struct {
	AuthorizationID string   `json:"authorization_id,omitempty"`
	StopSequence    int64    `json:"stop_sequence,omitempty"`
	StopRequested   bool     `json:"stop_requested,omitempty"`
	Stopped         bool     `json:"stopped,omitempty"`
	Process         Identity `json:"process"`
	JournalID       string   `json:"journal_id"`
	UpgradeID       string   `json:"upgrade_id"`
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
	cfg := s.cfg
	if cfg.Role != "runner" || s.state.CurrentJournal == "" {
		return cfg
	}
	cfg.DataFiles = slices.Clone(cfg.DataFiles)
	for i, path := range cfg.DataFiles {
		name := filepath.Base(path)
		if name == "journal.db" || name == "journal.db.incarnation" {
			cfg.DataFiles[i] = filepath.Join(filepath.Dir(path), "generations", s.state.CurrentJournal, name)
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

// Retire only processes for which the server has observed an empty journal.
// Persist permission before signalling and persist exit before acknowledging it.
func (s *Supervisor) retireDrained(ctx context.Context) error {
	var failures []error
	for key, drain := range s.state.DrainingProcesses {
		if key == s.state.Process.LaunchID || drain.Process.LaunchID != key || s.state.CurrentJournal == "" {
			continue
		}
		req := protocol.RunnerRetirement{UpgradeID: drain.UpgradeID, Process: drain.Process}
		if drain.AuthorizationID == "" {
			result, err := s.opts.RollingAuthority.Retire(ctx, s.state.CurrentJournal, req)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if !result.Allowed {
				continue
			}
			drain.AuthorizationID = result.AuthorizationID
			drain.StopSequence = result.ReadySequence
			s.state.DrainingProcesses[key] = drain
			if err := s.save(); err != nil {
				return err
			}
		}
		if !drain.Stopped {
			got, err := s.opts.Process.Inspect(ctx, drain.Process.PID)
			if err == nil && got == drain.Process && drain.StopRequested {
				// A lost acknowledgement alone cannot repeat a signal. A newer
				// ready heartbeat and the same exact live process reconcile an
				// interrupted attempt, including a crash before signal dispatch.
				observed, e := s.opts.RollingAuthority.Retire(ctx, s.state.CurrentJournal, req)
				if e != nil {
					failures = append(failures, e)
					continue
				}
				if !observed.Allowed || observed.AuthorizationID != drain.AuthorizationID || observed.ReadySequence <= drain.StopSequence {
					failures = append(failures, ErrUnknown)
					continue
				}
				drain.StopSequence = observed.ReadySequence
			}
			if err == nil && got == drain.Process {
				drain.StopRequested = true
				s.state.DrainingProcesses[key] = drain
				if err := s.save(); err != nil {
					return err
				}

				if err = s.opts.Process.Stop(ctx, drain.Process, time.Duration(s.cfg.StopTimeoutSeconds)*time.Second); err == nil {
					got, err = s.opts.Process.Inspect(ctx, drain.Process.PID)
				}
			}
			if !errors.Is(err, ErrNotRunning) && !(err == nil && got.StartTicks != 0 && got.StartTicks != drain.Process.StartTicks) {
				failures = append(failures, errors.Join(ErrUnknown, err))
				continue
			}
			drain.Stopped = true
			s.state.DrainingProcesses[key] = drain
			if err := s.save(); err != nil {
				return err
			}
		}
		req.AuthorizationID = drain.AuthorizationID
		result, err := s.opts.RollingAuthority.Retire(ctx, s.state.CurrentJournal, req)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !result.Confirmed {
			continue
		}
		delete(s.state.DrainingProcesses, key)
		if err := s.save(); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}
