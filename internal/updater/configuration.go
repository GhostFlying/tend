package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// StagedConfiguration contains identities and private file references, never content.
type StagedConfiguration struct {
	Target         protocol.RunnerConfiguration `json:"target"`
	ManifestSHA256 string                       `json:"manifest_sha256"`
	Path           string                       `json:"path"`
	Candidate      string                       `json:"candidate"`
	Backup         string                       `json:"backup"`
	Mode           uint32                       `json:"mode"`
}

func configurationDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return "", errors.New("updater: configuration must be a private regular file of at most 1 MiB")
	}
	return hashFile(path)
}

// StageConfiguration does not alter the managed configuration or service.
// The original backup is immutable and survives interruption before any restart.
func (s *Supervisor) StageConfiguration(ctx context.Context, id, manifest, candidate string, target *protocol.RunnerConfiguration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.durabilityError(); err != nil {
		return err
	}
	if s.cfg.ServiceConfig == "" || s.cfg.Role != "runner" || !release.ValidID(id) || !release.ValidSHA(manifest) || target == nil || !release.ValidSHA(target.PreviousSHA256) || !release.ValidSHA(target.SHA256) || target.PreviousSHA256 == target.SHA256 || !filepath.IsAbs(candidate) {
		return errors.New("updater: invalid configuration stage request")
	}
	if prior, ok := s.state.Configurations[id]; ok {
		if prior.ManifestSHA256 != manifest || !sameConfigurationTarget(prior.Target, *target) || prior.Path != s.cfg.ServiceConfig {
			return errors.New("updater: configuration stage identity conflict")
		}
		return nil
	}
	if _, ok := s.state.Terminals[id]; ok {
		return errors.New("updater: historical operation cannot stage configuration")
	}
	if !done(s.state.Operation) {
		return errors.New("updater: another operation is unfinished")
	}
	before, err := configurationDigest(s.cfg.ServiceConfig)
	if err != nil || before != target.PreviousSHA256 {
		return errors.New("updater: original configuration identity changed")
	}
	after, err := configurationDigest(candidate)
	if err != nil || after != target.SHA256 {
		return errors.New("updater: candidate configuration identity mismatch")
	}
	if actual, err := hashFile(s.state.Current.Binary); err != nil || actual != s.state.Current.Artifact.SHA256 {
		return errors.New("updater: installed validator identity changed")
	}
	validator := s.opts.ValidateConfiguration
	if validator == nil {
		validator = validateConfiguration
	}
	if err := validator(ctx, s.state.Current.Binary, s.cfg.ServiceConfig, candidate); err != nil {
		return errors.New("updater: configuration validation rejected")
	}
	info, err := os.Stat(s.cfg.ServiceConfig)
	if err != nil {
		return err
	}
	root := filepath.Join(s.cfg.StateDir, "configurations")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	// A unique private directory makes retries after a crash before save harmless.
	dir, err := os.MkdirTemp(root, id+"-")
	if err != nil {
		return err
	}
	staged := StagedConfiguration{Target: *target, ManifestSHA256: manifest, Path: s.cfg.ServiceConfig, Candidate: filepath.Join(dir, "candidate"), Backup: filepath.Join(dir, "original"), Mode: uint32(info.Mode().Perm())}
	if err := copyAtomic(candidate, staged.Candidate, 0600); err != nil {
		return err
	}
	if err := copyAtomic(s.cfg.ServiceConfig, staged.Backup, 0600); err != nil {
		return err
	}
	if err := s.verifyStagedConfiguration(&staged); err != nil {
		return err
	}
	if s.state.Configurations == nil {
		s.state.Configurations = map[string]StagedConfiguration{}
	}
	s.state.Configurations[id] = staged
	return s.save()
}

func (s *Supervisor) verifyStagedConfiguration(c *StagedConfiguration) error {
	if c.Path != s.cfg.ServiceConfig {
		return errors.New("updater: fixed configuration path changed")
	}
	for path, expected := range map[string]string{c.Path: c.Target.PreviousSHA256, c.Backup: c.Target.PreviousSHA256, c.Candidate: c.Target.SHA256} {
		actual, err := configurationDigest(path)
		if err != nil || actual != expected {
			return errors.New("updater: staged configuration no longer matches")
		}
	}
	return nil
}

func (s *Supervisor) applyConfiguration(c *StagedConfiguration) error {
	if c == nil || c.Path != s.cfg.ServiceConfig {
		return errors.New("updater: missing fixed configuration target")
	}
	old, err := configurationDigest(c.Path)
	if err != nil {
		return err
	}
	if old != c.Target.PreviousSHA256 && old != c.Target.SHA256 {
		return errors.New("updater: configuration compare-and-swap failed")
	}
	for path, expected := range map[string]string{c.Backup: c.Target.PreviousSHA256, c.Candidate: c.Target.SHA256} {
		sum, err := configurationDigest(path)
		if err != nil || sum != expected {
			return errors.New("updater: configuration backup or candidate changed")
		}
	}
	if old == c.Target.SHA256 {
		return nil
	}
	return copyAtomic(c.Candidate, c.Path, os.FileMode(c.Mode))
}

func (s *Supervisor) restoreConfiguration(c *StagedConfiguration) error {
	if c == nil {
		return nil
	}
	if c.Path != s.cfg.ServiceConfig {
		return errors.New("updater: fixed configuration path changed")
	}
	sum, err := configurationDigest(c.Backup)
	if err != nil || sum != c.Target.PreviousSHA256 {
		return errors.New("updater: original configuration backup changed")
	}
	current, err := configurationDigest(c.Path)
	if err != nil {
		return err
	}
	if current != c.Target.SHA256 && current != c.Target.PreviousSHA256 {
		return errors.New("updater: configuration changed outside maintenance")
	}
	if current == c.Target.PreviousSHA256 {
		return nil
	}
	return copyAtomic(c.Backup, c.Path, os.FileMode(c.Mode))
}

// Validation is a bounded read-only mode of the pinned managed runner binary.
func validateConfiguration(ctx context.Context, binary, previous, candidate string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, binary, "check-config", previous, candidate).Run()
}

func sameConfigurationTarget(a, b protocol.RunnerConfiguration) bool {
	return a.PreviousSHA256 == b.PreviousSHA256 && a.SHA256 == b.SHA256 && slices.Equal(a.Providers, b.Providers)
}
