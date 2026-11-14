package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/release"
)

// Launch receipts are immutable facts from this launcher's exec.Cmd, separate
// from Supervisor state. A Wait goroutine never advances an installation.
type ownedLaunchIntent struct {
	Format              int    `json:"format"`
	LaunchID            string `json:"launch_id"`
	VerifiedFDSHA256    string `json:"verified_fd_sha256"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
	ScopeSHA256         string `json:"scope_sha256"`
}

type ownedLaunchDispatch struct {
	IntentSHA256 string `json:"intent_sha256"`
	Nonce        string `json:"nonce"`
}

type ownedLaunchChild struct {
	Dispatch ownedLaunchDispatch `json:"dispatch"`
	PID      int                 `json:"pid"`
}

type ownedLaunchExit struct {
	Child ownedLaunchChild `json:"child"`
	Proof LaunchExitProof  `json:"proof"`
}

type ownedLaunchReceipt struct {
	dir      string
	intent   ownedLaunchIntent
	dispatch ownedLaunchDispatch
}

// LoadOwnedLaunchExit returns only a durable, original child's owned Wait fact.
// A missing receipt says nothing about whether a process started or is gone.
// The caller must separately fence descendants and retained native operations.
func LoadOwnedLaunchExit(cfg Config, launchID, artifactSHA string) (*LaunchExitProof, error) {
	if cfg.StateDir == "" {
		return nil, nil // Standalone Process users do not have durable ownership.
	}
	dir, err := ownedLaunchDirectory(cfg, launchID, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	intent, err := readOwnedIntent(dir, cfg, launchID, artifactSHA)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var exit ownedLaunchExit
	if err := readLaunchFact(filepath.Join(dir, "exit.json"), &exit); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var dispatch ownedLaunchDispatch
	var child ownedLaunchChild
	if err := readLaunchFact(filepath.Join(dir, "dispatch.json"), &dispatch); err != nil {
		return nil, fmt.Errorf("updater: owned exit dispatch: %w", err)
	}
	if err := readLaunchFact(filepath.Join(dir, "child.json"), &child); err != nil {
		return nil, fmt.Errorf("updater: owned exit child: %w", err)
	}
	if dispatch.IntentSHA256 != digest(intent) || !release.ValidID(dispatch.Nonce) || child.Dispatch != dispatch || child.PID <= 1 || exit.Child != child || exit.Proof.PID != child.PID || exit.Proof.LaunchID != launchID || exit.Proof.ArtifactSHA256 != artifactSHA || !validOwnedExit(exit.Proof) {
		return nil, errors.New("updater: owned launch exit receipt binding differs")
	}
	proof := exit.Proof
	return &proof, nil
}

func validOwnedExit(p LaunchExitProof) bool {
	return p.PID > 1 && release.ValidID(p.LaunchID) && release.ValidSHA(p.ArtifactSHA256) && p.ExitCode >= -1 && p.ExitCode <= 255 && p.Signal >= 0 && p.Signal <= 64 && ((p.Signal == 0 && p.ExitCode >= 0) || (p.Signal != 0 && p.ExitCode == -1))
}

// replayOwnedLaunch prevents a second exec after an uncertain original dispatch.
// An intent without a dispatch is the sole retryable pre-child case.
func replayOwnedLaunch(cfg Config, launchID, sha string) error {
	if cfg.StateDir == "" {
		return nil
	}
	dir, err := ownedLaunchDirectory(cfg, launchID, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: launch receipt directory: %v", ErrUnknown, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "dispatch.json")); errors.Is(err, os.ErrNotExist) {
		// A surviving child/exit without its dispatch is corruption, not a retry.
		for _, name := range []string{"child.json", "exit.json"} {
			if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: launch receipt has no dispatch", ErrUnknown)
			}
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: launch dispatch is unreadable", ErrUnknown)
	}
	proof, err := LoadOwnedLaunchExit(cfg, launchID, sha)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	if proof != nil {
		return &launchExitedError{proof: *proof}
	}
	return fmt.Errorf("%w: original launch was already dispatched", ErrUnknown)
}

func prepareOwnedLaunch(cfg Config, launchID, verifiedSHA string, extra map[string]string) (*ownedLaunchReceipt, error) {
	if cfg.StateDir == "" {
		return nil, nil
	}
	if !release.ValidSHA(verifiedSHA) {
		return nil, errors.New("updater: invalid verified launch executable digest")
	}
	dir, err := ownedLaunchDirectory(cfg, launchID, true)
	if err != nil {
		return nil, err
	}
	// Hash only the explicit execution scope; never persist environment values,
	// environment files or arbitrary provider credentials.
	scope := make(map[string]string)
	for _, key := range []string{"TEND_UPDATER_INSTANCE_ID", "TEND_UPDATER_PROCESS_INSTANCE_ID", "TEND_UPDATER_RELEASE_ID", "TEND_UPGRADE_ID", "TEND_UPDATER_MAINTENANCE", "TEND_COMMITTED_MAINTENANCE_ID", "TEND_FORWARD_RECOVERY_ID", "TEND_BOUNDED_RECOVERY_REQUEST_ID"} {
		if value, ok := extra[key]; ok {
			scope[key] = value
		}
	}
	intent := ownedLaunchIntent{Format: 1, LaunchID: launchID, VerifiedFDSHA256: verifiedSHA, ConfigurationSHA256: digest(cfg), ScopeSHA256: digest(scope)}
	if err := publishLaunchFact(filepath.Join(dir, "intent.json"), intent); err != nil {
		return nil, err
	}
	nonce, err := newID()
	if err != nil {
		return nil, err
	}
	r := &ownedLaunchReceipt{dir: dir, intent: intent, dispatch: ownedLaunchDispatch{IntentSHA256: digest(intent), Nonce: nonce}}
	if err := publishLaunchFact(filepath.Join(dir, "dispatch.json"), r.dispatch); err != nil {
		// A different nonce means another invocation already owns the dispatch.
		return nil, fmt.Errorf("%w: exclusive launch dispatch: %v", ErrUnknown, err)
	}
	return r, nil
}

func (r *ownedLaunchReceipt) child(pid int) ownedLaunchChild {
	return ownedLaunchChild{Dispatch: r.dispatch, PID: pid}
}

func (r *ownedLaunchReceipt) recordChild(pid int) error {
	if r == nil {
		return nil
	}
	return publishLaunchFact(filepath.Join(r.dir, "child.json"), r.child(pid))
}

func (r *ownedLaunchReceipt) recordExit(proof LaunchExitProof) error {
	if r == nil {
		return nil
	}
	if proof.LaunchID != r.intent.LaunchID || proof.ArtifactSHA256 != r.intent.VerifiedFDSHA256 || !validOwnedExit(proof) {
		return errors.New("updater: owned Wait differs from its verified launch intent")
	}
	if err := r.recordChild(proof.PID); err != nil {
		return err
	}
	err := publishLaunchFact(filepath.Join(r.dir, "exit.json"), ownedLaunchExit{Child: r.child(proof.PID), Proof: proof})
	if err == nil {
		slog.Info("updater owned process exited", "process_pid", proof.PID, "process_launch_id", proof.LaunchID, "artifact_sha256", proof.ArtifactSHA256, "exit_code", proof.ExitCode, "exit_signal", proof.Signal)
	}
	return err
}

// abandonUnstarted releases only this invocation's exclusive dispatch after
// cmd.Start definitely failed. The immutable original intent remains pinned.
func (r *ownedLaunchReceipt) abandonUnstarted() error {
	if r == nil {
		return nil
	}
	for _, name := range []string{"child.json", "exit.json"} {
		if _, err := os.Lstat(filepath.Join(r.dir, name)); !errors.Is(err, os.ErrNotExist) {
			return ErrUnknown
		}
	}
	var dispatch ownedLaunchDispatch
	if err := readLaunchFact(filepath.Join(r.dir, "dispatch.json"), &dispatch); err != nil {
		return err
	}
	if dispatch != r.dispatch {
		return ErrUnknown
	}
	if err := os.Remove(filepath.Join(r.dir, "dispatch.json")); err != nil {
		return err
	}
	return syncDir(r.dir)
}

func readOwnedIntent(dir string, cfg Config, launchID, sha string) (ownedLaunchIntent, error) {
	var intent ownedLaunchIntent
	if err := readLaunchFact(filepath.Join(dir, "intent.json"), &intent); err != nil {
		return intent, err
	}
	if intent.Format != 1 || intent.LaunchID != launchID || intent.VerifiedFDSHA256 != sha || !release.ValidSHA(sha) || intent.ConfigurationSHA256 != digest(cfg) || !release.ValidSHA(intent.ScopeSHA256) {
		return intent, errors.New("updater: owned launch intent binding differs")
	}
	return intent, nil
}

func readLaunchFact(path string, out any) error {
	raw, err := privateFile(path)
	if err != nil {
		return err
	}
	return strictJSON(raw, out)
}

func ownedLaunchDirectory(cfg Config, launchID string, create bool) (string, error) {
	if !filepath.IsAbs(cfg.StateDir) || filepath.Clean(cfg.StateDir) != cfg.StateDir || !release.ValidID(launchID) {
		return "", errors.New("updater: launch receipt requires an absolute private state directory and valid launch ID")
	}
	dir := cfg.StateDir
	for _, part := range []string{"", "launch-receipts", bytesDigest([]byte(launchID))} {
		if part != "" {
			dir = filepath.Join(dir, part)
			if create {
				if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
					return "", err
				}
				if err := syncDir(filepath.Dir(dir)); err != nil {
					return "", err
				}
			}
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !ownedFile(info) {
			return "", errors.New("updater: launch receipt directory must be private and operator-owned")
		}
	}
	return dir, nil
}

// publishLaunchFact never replaces a previous fact. Identical publication is
// idempotent; mismatches preserve the original unknown and fail closed.
func publishLaunchFact(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".launch-fact-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		prior, err := privateFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(prior, raw) {
			return errors.New("updater: immutable launch fact already has another binding")
		}
	}
	return syncDir(filepath.Dir(path))
}
