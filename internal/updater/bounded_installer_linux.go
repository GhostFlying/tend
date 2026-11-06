//go:build linux

package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type boundedInstallerJournal struct {
	ConfigSHA256 string                           `json:"config_sha256"`
	Action       string                           `json:"action"`
	Phase        string                           `json:"phase"`
	Window       protocol.BoundedMaintenanceState `json:"window"`
}

func boundedInstallerPath(cfg Config, id string) string {
	return filepath.Join(cfg.StateDir, "bounded-installers", id+".json")
}

func beginBoundedInstaller(cfg Config, a protocol.BoundedMaintenanceAuthorization, action string) (boundedInstallerJournal, error) {
	var j boundedInstallerJournal
	if !protocol.ValidID(protocol.PrefixRequest, a.RequestID) {
		return j, errors.New("updater: bounded installer request required")
	}
	path := boundedInstallerPath(cfg, a.RequestID)
	if raw, err := privateFile(path); err == nil {
		if err := strictJSON(raw, &j); err != nil {
			return j, err
		}
		if digest(j.Window.Authorization) != digest(a) || j.Action != action || j.ConfigSHA256 != digest(cfg) {
			return j, errors.New("updater: original bounded installer request changed")
		}
		return j, nil
	} else if !os.IsNotExist(err) {
		return j, err
	}
	if err := validateTimeoutPolicy(a.Timeouts); err != nil {
		return j, err
	}
	boot, mono, err := BootClock()
	if err != nil {
		return j, err
	}
	now := time.Now().UnixMilli()
	if a.ExpiresAtMillis < now+a.Timeouts.OverallMillis {
		return j, errors.New("updater: bounded authorization expires before its recovery window")
	}
	j = boundedInstallerJournal{ConfigSHA256: digest(cfg), Action: action, Phase: "planned", Window: protocol.BoundedMaintenanceState{Authorization: a, BootID: boot, OverallStartedAtMillis: now, OverallDeadlineMillis: now + a.Timeouts.OverallMillis, OverallDeadlineMono: mono + a.Timeouts.OverallMillis, Phase: "handoff", PhaseEnteredAtMillis: now, PhaseDeadlineMillis: now + a.Timeouts.HandoffMillis, PhaseDeadlineMono: mono + a.Timeouts.HandoffMillis}}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return j, err
	}
	return j, writeJSON(path, j)
}

func boundedInstallerContext(ctx context.Context, j boundedInstallerJournal) (context.Context, context.CancelFunc, error) {
	boot, mono, err := BootClock()
	if err != nil {
		return nil, nil, err
	}
	remaining := min(j.Window.OverallDeadlineMillis-time.Now().UnixMilli(), j.Window.OverallDeadlineMono-mono, j.Window.PhaseDeadlineMillis-time.Now().UnixMilli(), j.Window.PhaseDeadlineMono-mono)
	if boot != j.Window.BootID || remaining <= 0 {
		return nil, nil, errors.New("updater: original bounded installer deadline expired; explicit new CAS authority required")
	}
	child, cancel := context.WithTimeout(ctx, time.Duration(remaining)*time.Millisecond)
	return child, cancel, nil
}

func awaitBoundedAdoption(ctx context.Context, cfg Config, p CommittedHandoffPlan, j boundedInstallerJournal) (State, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
		if err != nil {
			return state, err
		}
		if state.Maintenance != nil && digest(state.Maintenance.Plan) == digest(p.Maintenance) {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return state, ErrUnknown
		case <-ticker.C:
		}
	}
}
