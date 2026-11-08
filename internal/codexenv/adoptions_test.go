//go:build linux

package codexenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionReferencesAreIndexedAndPaginated(t *testing.T) {
	s, p := fixture(t)
	create(t, s, p)
	b := &Binding{Directory: s.Root, ID: p.ID}
	for i := 0; i < 105; i++ {
		if _, err := b.Pin(t.Context(), fmt.Sprintf("run:%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Execute(t.Context(), Request{Action: "inventory", ID: p.ID, Limit: 40})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Status.Adoptions) != 40 || !first.Status.AdoptionsMore || first.Status.AdoptionsNext != "run:039" {
		t.Fatal("first adoption page incorrect")
	}
	second, err := s.Execute(t.Context(), Request{Action: "inventory", ID: p.ID, Limit: 40, After: first.Status.AdoptionsNext})
	if err != nil || len(second.Status.Adoptions) != 40 {
		t.Fatal("second adoption page incorrect", err)
	}
	if _, exists := second.Status.Adoptions["run:000"]; exists {
		t.Fatal("adoption cursor repeated first page")
	}
	raw, err := os.ReadFile(filepath.Join(s.Root, p.ID, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "run:104") {
		t.Fatal("execution history is copied into control state")
	}
}

func TestEnvironmentRejectsDefaultHomeAndNonexistentAliases(t *testing.T) {
	s, p := fixture(t)
	p.Home = filepath.Join(p.UserHome, ".codex")
	if _, err := s.Execute(t.Context(), Request{Action: "create", ID: p.ID, RequestID: "default", Profile: &p}); err == nil {
		t.Fatal("default app home accepted")
	}
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	if !samePath(filepath.Join(actual, "future"), filepath.Join(alias, "future")) {
		t.Fatal("nonexistent child alias was not normalized")
	}
}
