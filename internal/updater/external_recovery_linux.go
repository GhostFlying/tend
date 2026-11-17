//go:build linux

package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ExternalRecovery(ctx context.Context, cfg Config, request ExternalRecoveryRequest) (ExternalRecoveryRecord, error) {
	return externalRecovery(ctx, cfg, request, externalSystemd{})
}

func externalInstallerLock(cfg Config) (*os.File, error) { return installerLock(cfg) }

type externalSystemd struct{}

func (externalSystemd) Inspect(ctx context.Context, unit, config string) (externalRecoveryUnit, error) {
	if unit == "" {
		raw, err := (systemdUser{}).call(ctx, "list-units", "--all", "--type=service", "--plain", "--no-legend")
		if err != nil {
			return externalRecoveryUnit{}, err
		}
		var found []externalRecoveryUnit
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			candidate, err := (externalSystemd{}).Inspect(ctx, fields[0], config)
			if err == nil {
				found = append(found, candidate)
			}
		}
		if len(found) != 1 {
			return externalRecoveryUnit{}, errors.New("updater: select the exact service with --unit; configuration did not identify one unit")
		}
		return found[0], nil
	}
	if !strings.HasSuffix(unit, ".service") || strings.ContainsAny(unit, "/ \t\n") || strings.HasPrefix(unit, "-") {
		return externalRecoveryUnit{}, errors.New("updater: invalid service unit")
	}
	m, _, err := managerSnapshot(ctx, unit)
	if err != nil {
		return externalRecoveryUnit{}, err
	}
	_, tail, ok := strings.Cut(m["ExecStart"], "path=")
	binary, _, found := strings.Cut(tail, " ;")
	command := "argv[]=" + binary + " serve --config " + config + " ;"
	alternate := "argv[]=" + binary + " serve -config " + config + " ;"
	if !ok || !found || !filepath.IsAbs(binary) || m["Id"] != unit || m["LoadState"] != "loaded" || m["Type"] != "simple" || !strings.Contains(m["ExecStart"], command) && !strings.Contains(m["ExecStart"], alternate) {
		return externalRecoveryUnit{}, errors.New("updater: unit does not run the fixed supervisor configuration")
	}
	sum, err := unitDigest(ctx, unit)
	if err != nil {
		return externalRecoveryUnit{}, err
	}
	group := m["ControlGroup"]
	if group != "" {
		if group == "/" || !filepath.IsAbs(group) || filepath.Clean(group) != group {
			return externalRecoveryUnit{}, errors.New("updater: invalid service cgroup")
		}
		self, err := os.ReadFile("/proc/self/cgroup")
		if err != nil {
			return externalRecoveryUnit{}, err
		}
		for _, line := range strings.Split(string(self), "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) == 3 && (parts[2] == group || strings.HasPrefix(parts[2], group+"/")) {
				return externalRecoveryUnit{}, errors.New("updater: recovery must run outside the target service")
			}
		}
	}
	return externalRecoveryUnit{Name: unit, Binary: binary, Digest: sum, Cgroup: group}, nil
}

func verifyExternalUnit(ctx context.Context, original externalRecoveryUnit, config string) error {
	current, err := (externalSystemd{}).Inspect(ctx, original.Name, config)
	if err != nil {
		return err
	}
	if current.Name != original.Name || current.Binary != original.Binary || current.Digest != original.Digest || current.Cgroup != "" && original.Cgroup != "" && current.Cgroup != original.Cgroup {
		return errors.New("updater: service identity changed during recovery")
	}
	return nil
}

func (externalSystemd) Stop(ctx context.Context, unit externalRecoveryUnit, config string) error {
	if err := verifyExternalUnit(ctx, unit, config); err != nil {
		return err
	}
	// Stop disables automatic restart. The explicit force applies to this
	// service's entire cgroup, including an unresponsive old supervisor/worker.
	if _, err := (systemdUser{}).call(ctx, "--no-block", "stop", unit.Name); err != nil {
		return err
	}
	_, killErr := (systemdUser{}).call(ctx, "kill", "--kill-whom=all", "--signal=SIGKILL", unit.Name)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		m, _, err := managerSnapshot(ctx, unit.Name)
		if err != nil {
			return err
		}
		empty := true
		if unit.Cgroup != "" {
			raw, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", unit.Cgroup, "cgroup.events"))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			empty = os.IsNotExist(err) || strings.Contains(string(raw), "populated 0")
		}
		if (m["ActiveState"] == "inactive" || m["ActiveState"] == "failed") && m["MainPID"] == "0" && empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), killErr)
		case <-ticker.C:
		}
	}
}

func (externalSystemd) Start(ctx context.Context, unit externalRecoveryUnit, config string) error {
	if err := verifyExternalUnit(ctx, unit, config); err != nil {
		return err
	}
	_, err := (systemdUser{}).call(ctx, "start", unit.Name)
	return err
}
