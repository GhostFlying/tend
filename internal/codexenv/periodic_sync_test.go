//go:build linux

package codexenv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomaticSyncAlwaysImportsSelectedPackageEdits(t *testing.T) {
	s, p := fixture(t)
	source := skill(t, t.TempDir(), "selected", "v1")
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: source}}}
	old := create(t, s, p)
	if defaults(Profile{}).IntervalSeconds != 21600 {
		t.Fatal("default is not six hours")
	}
	if err := s.SyncAutomatic(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	stable, _ := s.Read(p.ID)
	if stable.Latest != old.Latest || len(stable.Snapshots) != 1 || stable.Revision != old.Revision+1 {
		t.Fatal("stable full sync did not reuse snapshot", stable)
	}
	skill(t, filepath.Dir(source), "selected", "v2")
	skill(t, filepath.Dir(source), "unselected", "excluded")
	if err := s.SyncAutomatic(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.Read(p.ID)
	if updated.Latest == old.Latest {
		t.Fatal("in-place edit was not synchronized")
	}
	for _, shot := range updated.Snapshots {
		if shot.ID == updated.Latest && len(shot.Packages) != 1 {
			t.Fatal("unselected package was enabled")
		}
	}
	raw, err := os.ReadFile(filepath.Join(source, "SKILL.md"))
	if err != nil || len(raw) == 0 {
		t.Fatal("sync changed original source", err)
	}
}

func TestWatchSchedulesFromCompletedFullSync(t *testing.T) {
	s, p := fixture(t)
	source := skill(t, t.TempDir(), "selected", "v1")
	p.IntervalSeconds = 60
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: source}}}
	create(t, s, p)
	clock := time.Unix(1000, 0)
	s.Now = func() time.Time { return clock }
	skill(t, filepath.Dir(source), "selected", "v2")
	s.Validate = func(context.Context, Profile, string) error { clock = clock.Add(2 * time.Minute); return nil }
	next := map[string]time.Time{}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.syncDue(t.Context(), all, next); err != nil {
		t.Fatal(err)
	}
	if !next[p.ID].Equal(clock.Add(time.Minute)) {
		t.Fatal("next due time was anchored to start", next, clock)
	}
	before, _ := s.Read(p.ID)
	if err = s.syncDue(t.Context(), all, next); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Read(p.ID)
	if after.Revision != before.Revision {
		t.Fatal("slow sync triggered an immediate repeated round")
	}
}
