package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/GhostFlying/tend/internal/release"
)

type ExternalRecoveryRequest struct {
	Release    string `json:"release"`
	ConfigFile string `json:"config_file"`
	Unit       string `json:"unit,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Force      bool   `json:"force"`
	Status     bool   `json:"status"`
}

type externalRecoveryUnit struct {
	Name   string `json:"name"`
	Binary string `json:"binary"`
	Digest string `json:"digest"`
	Cgroup string `json:"cgroup"`
}

// ExternalRecoveryRecord survives loss of the installer reply. The previous
// updater bytes are archived independently of whether their schema is readable.
type ExternalRecoveryRecord struct {
	RequestID     string               `json:"request_id"`
	Phase         string               `json:"phase"`
	ConfigSHA256  string               `json:"config_sha256"`
	ReleaseSHA256 string               `json:"release_sha256"`
	Unit          externalRecoveryUnit `json:"unit"`
	Target        Version              `json:"target"`
	LaunchID      string               `json:"launch_id"`
	Archive       string               `json:"archive"`
	BackupDir     string               `json:"backup_dir,omitempty"`
}

type externalRecoveryManager interface {
	Inspect(context.Context, string, string) (externalRecoveryUnit, error)
	Stop(context.Context, externalRecoveryUnit, string) error
	Start(context.Context, externalRecoveryUnit, string) error
}

func externalRecovery(ctx context.Context, cfg Config, request ExternalRecoveryRequest, manager externalRecoveryManager) (ExternalRecoveryRecord, error) {
	var record ExternalRecoveryRecord
	if err := validateServiceConfig(cfg); err != nil {
		return record, err
	}
	if (!request.Force && !request.Status) || !filepath.IsAbs(request.ConfigFile) || !filepath.IsAbs(cfg.StateDir) || cfg.AuthoritySocket == "" || cfg.Role != "server" && cfg.Role != "runner" {
		return record, errors.New("updater: external recovery requires --force, the fixed config and a private readiness socket")
	}
	bundle, err := release.Load(request.Release)
	if err != nil {
		return record, err
	}
	if bundle.Set == nil || bundle.Set.Format < 2 {
		return record, errors.New("updater: external recovery requires a complete release set")
	}
	artifact, err := bundle.Artifact(cfg.Role, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return record, err
	}
	installer, err := bundle.SetArtifact("updater", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return record, err
	}
	configHash, err := hashFile(request.ConfigFile)
	if err != nil {
		return record, err
	}
	if request.RequestID == "" {
		request.RequestID = "recover-" + digest([]string{cfg.InstanceID, configHash, bundle.SetSHA256})[:32]
	}
	if !release.ValidID(request.RequestID) {
		return record, errors.New("updater: invalid recovery identity")
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return record, err
	}
	info, err := os.Lstat(cfg.StateDir)
	if err != nil {
		return record, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return record, errors.New("updater: recovery state directory must be private")
	}
	lock, err := externalInstallerLock(cfg)
	if err != nil {
		return record, err
	}
	defer unlock(lock)
	path := filepath.Join(cfg.StateDir, "external-"+request.RequestID+".json")
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := strictJSON(raw, &record); err != nil {
			return record, err
		}
		if record.RequestID != request.RequestID || record.ConfigSHA256 != configHash || record.ReleaseSHA256 != bundle.SetSHA256 || request.Unit != "" && record.Unit.Name != request.Unit {
			return record, errors.New("updater: original recovery input changed")
		}
	} else if !os.IsNotExist(err) {
		return record, err
	} else {
		if request.Status {
			return record, errors.New("updater: original recovery record was not found")
		}
		unit, err := manager.Inspect(ctx, request.Unit, request.ConfigFile)
		if err != nil {
			return record, err
		}
		target, err := installVersion(cfg.StateDir, bundle, artifact)
		if err != nil {
			return record, err
		}
		launch, err := newID()
		if err != nil {
			return record, err
		}
		record = ExternalRecoveryRecord{RequestID: request.RequestID, Phase: "prepared", ConfigSHA256: configHash, ReleaseSHA256: bundle.SetSHA256, Unit: unit, Target: target, LaunchID: launch, Archive: filepath.Join(cfg.StateDir, "archives", request.RequestID)}
		if err := writeJSON(path, record); err != nil {
			return record, err
		}
	}
	if request.Status {
		return record, nil
	}
	verifyConfig := func() error {
		current, err := hashFile(request.ConfigFile)
		if err != nil {
			return err
		}
		if current != record.ConfigSHA256 {
			return errors.New("updater: configuration changed during recovery")
		}
		return nil
	}
	advance := func(phase string) error { record.Phase = phase; return writeJSON(path, record) }
	if record.Phase == "complete" {
		return record, nil
	}
	if record.Phase == "prepared" {
		// The bootstrap config is not the installed schema. When the old state
		// is unreadable, retain selected data and let the candidate verify it.
		storage := int64(0)
		if old, err := readState(filepath.Join(cfg.StateDir, "state.json")); err == nil && old.InstanceID == cfg.InstanceID {
			storage = old.Current.StorageVersion
		}
		readable := bundle.Manifest.Storage.ServerSchema
		if cfg.Role == "runner" {
			readable = bundle.Manifest.Storage.RunnerJournal
		}
		if storage > 0 && !readable.Contains(storage) {
			return record, errors.New("updater: candidate cannot read the installed storage")
		}
		if err := advance("stopping"); err != nil {
			return record, err
		}
	}
	if record.Phase == "stopping" {
		if err := verifyConfig(); err != nil {
			return record, err
		}
		if err := manager.Stop(ctx, record.Unit, request.ConfigFile); err != nil {
			return record, err
		}
		if err := advance("stopped"); err != nil {
			return record, err
		}
	}
	if record.Phase == "stopped" {
		if err := verifyConfig(); err != nil {
			return record, err
		}
		// The original flock remains the sole-writer boundary even when the old
		// JSON cannot be decoded. Never delete or replace its lock inode.
		supervisorLock, err := lockDir(cfg.StateDir)
		if err != nil {
			return record, err
		}
		defer unlock(supervisorLock)
		if err := os.MkdirAll(record.Archive, 0700); err != nil {
			return record, err
		}
		archive := filepath.Join(record.Archive, "state.json")
		statePath := filepath.Join(cfg.StateDir, "state.json")
		if _, err := os.Stat(archive); os.IsNotExist(err) {
			if raw, err := os.ReadFile(statePath); err == nil {
				if err := atomicWrite(archive, raw, 0600); err != nil {
					return record, err
				}
			} else if !os.IsNotExist(err) {
				return record, err
			}
		} else if err != nil {
			return record, err
		}
		if err := archiveRecoveryStartupJournals(cfg, record.Archive); err != nil {
			return record, err
		}
		storage := int64(0)
		if old, err := readState(archive); err == nil && old.InstanceID == cfg.InstanceID {
			storage = old.Current.StorageVersion
		}
		range_ := bundle.Manifest.Storage.ServerSchema
		if cfg.Role == "runner" {
			range_ = bundle.Manifest.Storage.RunnerJournal
		}
		if storage > 0 && !range_.Contains(storage) {
			return record, errors.New("updater: candidate cannot read the installed storage")
		}
		op := &Operation{ID: record.RequestID, Kind: "recover", Phase: "backed_up", Candidate: record.Target, LaunchID: record.LaunchID, Irreversible: true, StartedAt: time.Now()}
		op.Binding = Binding{UpgradeID: record.RequestID, InstanceID: cfg.InstanceID, ProcessInstanceID: record.LaunchID, ReleaseID: record.Target.ReleaseID, ManifestSHA256: record.Target.ManifestSHA256, ArtifactSHA256: artifact.SHA256}
		if storage != record.Target.StorageVersion {
			op.BackupDir = filepath.Join(cfg.StateDir, "backups", record.RequestID)
			if err := backupDataContext(ctx, cfg, op); err != nil {
				return record, err
			}
			record.BackupDir = op.BackupDir
		}
		// The ordinary supervisor launch, process discovery, readiness and durable
		// commit path owns the candidate from this point onward.
		state := State{Format: 1, InstanceID: cfg.InstanceID, Current: record.Target, Operation: op}
		if err := writeJSON(statePath, state); err != nil {
			return record, err
		}
		candidate, err := release.RegularPath(bundle.Dir, installer.Path)
		if err != nil {
			return record, err
		}
		if err := copyAtomic(candidate, record.Unit.Binary, 0755); err != nil {
			return record, err
		}
		if sum, err := hashFile(record.Unit.Binary); err != nil || sum != installer.SHA256 {
			return record, errors.New("updater: installed supervisor differs from the selected release")
		}
		if err := advance("installed"); err != nil {
			return record, err
		}
		// Release before starting the fixed supervisor; the installer lock stays.
		if err := unlock(supervisorLock); err != nil {
			return record, err
		}
	}
	if record.Phase == "installed" {
		if err := verifyConfig(); err != nil {
			return record, err
		}
		if err := manager.Start(ctx, record.Unit, request.ConfigFile); err != nil {
			return record, err
		}
		if err := advance("started"); err != nil {
			return record, err
		}
	}
	if record.Phase != "started" {
		return record, errors.New("updater: unknown external recovery phase")
	}
	// An unavailable reply preserves the started identity. Retrying reads this
	// operation instead of stopping the newly launched process again.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
		if err == nil && state.InstanceID == cfg.InstanceID && state.Operation != nil && state.Operation.ID == record.RequestID && state.Operation.Phase == "committed" && state.Current == record.Target && state.Process.LaunchID == record.LaunchID {
			err := advance("complete")
			return record, err
		}
		select {
		case <-ctx.Done():
			return record, ctx.Err()
		case <-ticker.C:
		}
	}
}

// These files are startup adoption inputs, not business data. Preserve their
// bytes after the old writers exit, so a corrupt or stale handoff cannot adopt
// the independently installed supervisor on its next start.
func archiveRecoveryStartupJournals(cfg Config, archive string) error {
	for _, name := range []string{"self-update", "self-update.json", "handoff.json", "committed-handoff.json", "committed-handoffs"} {
		source, target := filepath.Join(cfg.StateDir, name), filepath.Join(archive, name)
		if _, err := os.Lstat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := os.Lstat(target); err == nil {
			return errors.New("updater: archived startup journal already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(source, target); err != nil {
			return err
		}
	}
	if err := syncDir(archive); err != nil {
		return err
	}
	return syncDir(cfg.StateDir)
}
