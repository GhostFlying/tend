package codexenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
)

// Watch performs bounded filesystem synchronization, never model work.
func (s *Service) Watch(ctx context.Context) error {
	if err := s.checkPlatform(ctx); err != nil {
		return err
	}
	next := map[string]time.Time{}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		all, err := s.List()
		if err != nil {
			return err
		}
		for _, status := range all {
			p := status.Profile
			if status.Deleted || !p.Enabled || (p.AutoSync != nil && !*p.AutoSync) || s.now().Before(next[p.ID]) {
				continue
			}
			next[p.ID] = s.now().Add(time.Duration(p.IntervalSeconds) * time.Second)
			if err = s.SyncAutomatic(ctx, p.ID); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (s *Service) SyncAutomatic(ctx context.Context, id string) error {
	status, err := s.Read(id)
	if err != nil {
		return err
	}
	if status.SyncHeld {
		return nil
	}
	if len(status.PendingRequestIDs) > 0 {
		pending := status.PendingRequestIDs[0]
		if !strings.HasPrefix(pending, "auto-") {
			return fmt.Errorf("manual request requires reconciliation: %s", pending)
		}
		_, err = s.Execute(ctx, Request{Action: "reconcile", ID: id, RequestID: pending})
		return err
	}
	stamp, err := sourceStamp(ctx, status.Profile)
	if err != nil {
		s.recordFailure(ctx, id, err)
		return err
	}
	if stamp == status.SourceStamp {
		return nil
	}
	preview, err := s.inspect(ctx, Request{Action: "plan", ID: id})
	if err != nil {
		s.recordFailure(ctx, id, err)
		return err
	}
	// A known validation failure can be retried on the next scheduled interval.
	// Pending requests above always retain their original identity instead.
	interval := int64(status.Profile.IntervalSeconds)
	requestID := "auto-" + digest([]byte(fmt.Sprintf("%s:%d:%s:%d", id, status.Revision, preview.Candidate.ID, s.now().Unix()/interval)))
	_, err = s.Execute(ctx, Request{Action: "sync", ID: id, RequestID: requestID, ExpectedRevision: status.Revision})
	return err
}
func (s *Service) recordFailure(ctx context.Context, id string, cause error) {
	dir, err := s.directory(id)
	if err != nil {
		return
	}
	unlock, err := lock(ctx, dir)
	if err != nil {
		return
	}
	defer unlock()
	path := filepath.Join(dir, "state.json")
	r, err := loadRecord(path)
	if err == nil {
		r.Status.LastError = cause.Error()
		_ = writeJSON(path, r)
	}
}
func systemdQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "\n", `\n`, "\r", `\r`).Replace(s) + `"`
}

func watchUnitName(root string) string {
	return "tend-codex-env-" + digest([]byte(canonicalPath(root)))[:12] + ".service"
}

type watchPreflightError struct{ cause error }

func (e *watchPreflightError) Error() string { return e.cause.Error() }
func (e *watchPreflightError) Unwrap() error { return e.cause }

func (s *Service) configureWatch(ctx context.Context, enabled bool) error {
	if enabled {
		if err := s.checkPlatform(ctx); err != nil {
			return &watchPreflightError{cause: err}
		}
	}
	if !filepath.IsAbs(s.UserHome) || !filepath.IsAbs(s.Binary) {
		return &watchPreflightError{cause: fmt.Errorf("watch requires absolute operator home and tend executable")}
	}
	name := watchUnitName(s.Root)
	path := filepath.Join(s.UserHome, ".config", "systemd", "user", name)
	if enabled {
		unit := "[Unit]\nDescription=Tend Codex environment synchronization\n[Service]\nType=simple\nEnvironment=" + systemdQuote("PATH="+os.Getenv("PATH")) + "\nExecStart=" + systemdQuote(s.Binary) + " codex-env watch run --root " + systemdQuote(canonicalPath(s.Root)) + "\nRestart=on-failure\nRestartSec=5\n[Install]\nWantedBy=default.target\n"
		if err := atomicWrite(path, []byte(unit)); err != nil {
			return err
		}
	}
	commands := [][]string{{"--user", "daemon-reload"}}
	if enabled {
		commands = append(commands, []string{"--user", "enable", "--now", name})
	} else {
		commands = append(commands, []string{"--user", "disable", "--now", name})
	}
	for _, args := range commands {
		if s.Systemctl != nil {
			if err := s.Systemctl(ctx, args); err != nil {
				return err
			}
			continue
		}
		cmd := exec.CommandContext(ctx, "systemctl", args...)
		procx.Contain(cmd)
		err := cmd.Run()
		procx.KillGroup(cmd)
		if err != nil {
			return fmt.Errorf("systemd watch transition requires reconciliation: %w", err)
		}
	}
	return nil
}

func (s *Service) checkPlatform(ctx context.Context) error {
	if s.Platform != nil {
		return s.Platform(ctx)
	}
	return platformCheck(ctx)
}

func (s *Service) importItems(ctx context.Context, id, kind string, items []Item) ([]Item, error) {
	out := append([]Item(nil), items...)
	dir, _ := s.directory(id)
	for i, item := range out {
		if !filepath.IsAbs(item.Path) {
			return nil, fmt.Errorf("local package source must be absolute")
		}
		stage, err := os.MkdirTemp(dir, ".import-")
		if err != nil {
			return nil, err
		}
		if err = materialize(ctx, item.Path, stage); err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
		sha, err := treeDigest(ctx, stage)
		if err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
		verify, err := os.MkdirTemp(dir, ".verify-import-")
		if err == nil {
			err = materialize(ctx, item.Path, verify)
		}
		if err == nil {
			var second string
			second, err = treeDigest(ctx, verify)
			if err == nil && second != sha {
				err = fmt.Errorf("installed source changed while copying")
			}
		}
		_ = os.RemoveAll(verify)
		if err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
		target := filepath.Join(dir, "imports", kind, digest([]byte(item.ID))[:16], sha)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err == nil {
			if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
				if err = syncTree(ctx, stage); err == nil {
					err = os.Rename(stage, target)
				}
				if err == nil {
					err = syncParents(target, s.Root)
				}
			}
		}
		_ = os.RemoveAll(stage)
		if err != nil {
			return nil, err
		}
		out[i].Path = target
	}
	return out, nil
}
