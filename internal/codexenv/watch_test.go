//go:build linux

package codexenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatchServicePersistsAndReconcilesOriginalRequest(t *testing.T) {
	s, _ := fixture(t)
	s.Binary = "/opt/tend/bin/tend"
	fail := true
	calls := 0
	s.Systemctl = func(context.Context, []string) error {
		calls++
		if fail {
			return context.DeadlineExceeded
		}
		return nil
	}
	q := Request{Action: "watch enable", ID: "watch-control", RequestID: "watch-original"}
	if _, err := s.Execute(t.Context(), q); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	status, err := s.Read("watch-control")
	if err != nil || len(status.PendingRequestIDs) != 1 {
		t.Fatal(status, err)
	}
	fail = false
	result, err := s.Execute(t.Context(), Request{Action: "reconcile", ID: "watch-control", RequestID: q.RequestID})
	if err != nil || !result.Status.WatchEnabled {
		t.Fatal(result, err)
	}
	before := calls
	if _, err = s.Execute(t.Context(), q); err != nil || calls != before {
		t.Fatal("completed service request repeated external effect", err)
	}
	files, err := filepath.Glob(filepath.Join(s.UserHome, ".config/systemd/user/*.service"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	unit, err := os.ReadFile(files[0])
	if err != nil || !strings.Contains(string(unit), "codex-env watch run --root") {
		t.Fatal("persistent user service missing", err)
	}
	if !strings.Contains(string(unit), systemdQuote("PATH="+os.Getenv("PATH"))) {
		t.Fatal("watch lost the operator's native tool discovery PATH")
	}
	s.Platform = func(context.Context) error { return ErrUnsupported }
	if _, err = s.Execute(t.Context(), Request{Action: "watch disable", ID: "watch-control", RequestID: "disable", ExpectedRevision: result.Status.Revision}); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherIdentityUsesCanonicalStorePath(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "store")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	if watchUnitName(actual) != watchUnitName(actual+"/") || watchUnitName(actual) != watchUnitName(alias) {
		t.Fatal("equivalent roots select different watcher services")
	}
}

func TestAutomaticSyncRetriesKnownFailureButReconcilesPendingIdentity(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			s, p := fixture(t)
			source := skill(t, t.TempDir(), "selected", "v1")
			p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: source}}}
			old := create(t, s, p)
			skill(t, filepath.Dir(source), "selected", "v2")
			s.Validate = func(context.Context, Profile, string) error {
				if pending {
					return context.DeadlineExceeded
				}
				return errors.New("temporarily unavailable")
			}
			if err := s.SyncAutomatic(t.Context(), p.ID); err == nil {
				t.Fatal("probe failure accepted")
			}
			dir, _ := s.directory(p.ID)
			before, _ := loadRecord(filepath.Join(dir, "state.json"))
			s.Validate = func(context.Context, Profile, string) error { return nil }
			s.Now = func() time.Time { return time.Unix(183, 0) }
			if err := s.SyncAutomatic(t.Context(), p.ID); err != nil {
				t.Fatal(err)
			}
			after, _ := loadRecord(filepath.Join(dir, "state.json"))
			if after.Status.Latest == old.Latest || len(after.Requests) != len(before.Requests)+map[bool]int{false: 1, true: 0}[pending] {
				t.Fatal("automatic retry lost original pending identity or did not retry known failure")
			}
		})
	}
}

func TestWatchRunsOnlyEnabledEnvironmentsAndStopsWithContext(t *testing.T) {
	s, p := fixture(t)
	source := skill(t, t.TempDir(), "selected", "v1")
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: source}}}
	old := create(t, s, p)
	skill(t, filepath.Dir(source), "selected", "v2")
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if err := s.Watch(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	now, err := s.Read(p.ID)
	if err != nil || now.Latest == old.Latest {
		t.Fatal("watch did not synchronize selected source", err)
	}
	before := now.Latest
	now.Profile.Enabled = false
	if _, err = s.Execute(t.Context(), Request{Action: "update", ID: p.ID, RequestID: "disable-profile", ExpectedRevision: now.Revision, Profile: &now.Profile}); err != nil {
		t.Fatal(err)
	}
	skill(t, filepath.Dir(source), "selected", "v3")
	ctx, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_ = s.Watch(ctx)
	after, _ := s.Read(p.ID)
	if after.Latest != before {
		t.Fatal("disabled environment was synchronized")
	}
}
