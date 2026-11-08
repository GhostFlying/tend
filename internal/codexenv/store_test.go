//go:build linux

package codexenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Service, Profile) {
	t.Helper()
	root := t.TempDir()
	user := filepath.Join(root, "user")
	if err := os.MkdirAll(user, 0700); err != nil {
		t.Fatal(err)
	}
	s := &Service{Root: filepath.Join(root, "environments"), UserHome: user, Now: func() time.Time { return time.Unix(123, 0) }, Validate: func(context.Context, Profile, string) error { return nil }}
	s.Platform = func(context.Context) error { return nil }
	p := Profile{ID: "worker", Home: filepath.Join(root, "runtime"), UserHome: user, Enabled: true}
	return s, p
}
func skill(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: fixture workflow\n---\n"+body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func create(t *testing.T, s *Service, p Profile) Status {
	t.Helper()
	result, err := s.Execute(t.Context(), Request{Action: "create", ID: p.ID, RequestID: "create", Profile: &p})
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.Read(result.Status.Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestCleanOwnedInstallationDoesNotFollowSource(t *testing.T) {
	s, p := fixture(t)
	source := skill(t, t.TempDir(), "own", "v1")
	p.Skills.Installed = []Item{{ID: "own", Path: source}}
	preview, err := s.Execute(t.Context(), Request{Action: "plan", ID: p.ID, Profile: &p})
	if err != nil {
		t.Fatal(err)
	}
	status := create(t, s, p)
	if status.Latest != preview.Candidate.ID {
		t.Fatal("independent install changed the previewed snapshot identity")
	}
	if status.Latest == "" || len(status.Snapshots) != 1 {
		t.Fatal(status)
	}
	_ = os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("invalid changed source"), 0600)
	result, err := s.Execute(t.Context(), Request{Action: "sync", ID: p.ID, RequestID: "sync", ExpectedRevision: status.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Status.Latest != status.Latest {
		t.Fatal("clean install followed original source")
	}
	if samePath(result.Status.Profile.Skills.Installed[0].Path, source) {
		t.Fatal("installation retains mutable source")
	}
}

func TestRollbackHoldsSnapshotUntilExplicitSync(t *testing.T) {
	s, p := fixture(t)
	source := skill(t, t.TempDir(), "selected", "v1")
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: source}}}
	old := create(t, s, p)
	skill(t, filepath.Dir(source), "selected", "v2")
	if err := s.SyncAutomatic(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	latest, _ := s.Read(p.ID)
	rolled, err := s.Execute(t.Context(), Request{Action: "rollback", ID: p.ID, RequestID: "rollback", ExpectedRevision: latest.Revision, Generation: old.Latest})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SyncAutomatic(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	held, _ := s.Read(p.ID)
	if held.Latest != old.Latest || !held.SyncHeld {
		t.Fatal("watch immediately undid explicit rollback")
	}
	resumed, err := s.Execute(t.Context(), Request{Action: "sync", ID: p.ID, RequestID: "resume", ExpectedRevision: rolled.Status.Revision})
	if err != nil || resumed.Status.Latest != latest.Latest || resumed.Status.SyncHeld {
		t.Fatal("explicit sync did not release rollback hold", err)
	}
}

func TestIndependentManagementImportsUpdatesAndRemovesLocally(t *testing.T) {
	s, p := fixture(t)
	status := create(t, s, p)
	source := skill(t, t.TempDir(), "own", "v1")
	manage := func(action, request, path string) {
		t.Helper()
		result, err := s.Execute(t.Context(), Request{Action: action, ID: p.ID, Item: Item{ID: "own", Path: path}, RequestID: request, ExpectedRevision: status.Revision})
		if err != nil {
			t.Fatal(err)
		}
		status = *result.Status
	}
	manage("skill add", "add", source)
	if samePath(status.Profile.Skills.Installed[0].Path, source) {
		t.Fatal("add did not import an independent copy")
	}
	first := status.Latest
	skill(t, filepath.Dir(source), "own", "v2")
	manage("skill update", "update", source)
	if status.Latest == first {
		t.Fatal("explicit update did not replace package")
	}
	manage("skill remove", "remove", "")
	if len(status.Profile.Skills.Installed) != 0 {
		t.Fatal("remove retained activation")
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		t.Fatal("remove changed source", err)
	}
}

func TestProjectionUpdatesOnlySelectedCompletePackages(t *testing.T) {
	s, p := fixture(t)
	sources := t.TempDir()
	source := skill(t, sources, "selected", "v1")
	p.SourceSkills = sources
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected"}}}
	status := create(t, s, p)
	ref, err := (&Binding{Directory: s.Root, ID: p.ID}).Pin(t.Context(), "run:original")
	if err != nil {
		t.Fatal(err)
	}
	skill(t, sources, "unselected", "new")
	if err = s.SyncAutomatic(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := s.Read(p.ID)
	if unchanged.Latest != status.Latest {
		t.Fatal("unselected addition changed snapshot")
	}
	skill(t, sources, "selected", "v2")
	if err = s.SyncAutomatic(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.Read(p.ID)
	if updated.Latest == status.Latest {
		t.Fatal("selected update not published")
	}
	again, err := (&Binding{Directory: s.Root, ID: p.ID}).Pin(t.Context(), "run:original")
	if err != nil || again.Generation != ref.Generation {
		t.Fatal("run adopted new generation during recovery", err)
	}
	newRun, err := (&Binding{Directory: s.Root, ID: p.ID}).Pin(t.Context(), "run:new")
	if err != nil || newRun.Generation != updated.Latest {
		t.Fatal("new run did not adopt latest", err)
	}
	oldFile := filepath.Join(ref.Directory, "generations", ref.Generation, "skills", digest([]byte("selected"))[:16], "scripts", "run.sh")
	if _, err = os.Stat(oldFile); err != nil {
		t.Fatal("complete historical package missing", err)
	}
	if err = os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncAutomatic(t.Context(), p.ID); err == nil {
		t.Fatal("missing projection silently removed")
	}
	failed, _ := s.Read(p.ID)
	if failed.Latest != updated.Latest || failed.LastError == "" {
		t.Fatal("failure lost last valid generation")
	}
}

func TestFaultRequestReplayAndReconciliation(t *testing.T) {
	s, p := fixture(t)
	failed := true
	s.Validate = func(context.Context, Profile, string) error {
		if failed {
			return context.DeadlineExceeded
		}
		return nil
	}
	q := Request{Action: "create", ID: p.ID, RequestID: "original", Profile: &p}
	if _, err := s.Execute(t.Context(), q); err == nil {
		t.Fatal("probe failure accepted")
	}
	failed = false
	result, err := s.Execute(t.Context(), Request{Action: "reconcile", ID: p.ID, RequestID: q.RequestID})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Execute(t.Context(), q)
	if err != nil || again.Status.Revision != result.Status.Revision {
		t.Fatal("request replay mutated state", err)
	}
	q.Profile = &Profile{ID: p.ID}
	if _, err = s.Execute(t.Context(), q); !errors.Is(err, ErrConflict) {
		t.Fatal("same request with changed input accepted", err)
	}
	if _, err = s.Execute(t.Context(), Request{Action: "sync", ID: p.ID, RequestID: "stale", ExpectedRevision: 0}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale control revision accepted", err)
	}
}

func TestPlanDoesNotInitializeStoreOrRuntime(t *testing.T) {
	s, p := fixture(t)
	q := Request{Action: "plan", ID: p.ID, Profile: &p}
	result, err := s.Execute(t.Context(), q)
	if err != nil || result.Candidate == nil {
		t.Fatal(result, err)
	}
	for _, path := range []string{s.Root, p.Home} {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("preview modified managed path", path, err)
		}
	}
}

func TestAgentsIndependentAndSync(t *testing.T) {
	for _, mode := range []string{"independent", "sync"} {
		t.Run(mode, func(t *testing.T) {
			s, p := fixture(t)
			source := filepath.Join(t.TempDir(), "AGENTS.md")
			_ = os.WriteFile(source, []byte("original"), 0600)
			p.Agents = Agents{Mode: mode, Source: source}
			before := create(t, s, p)
			_ = os.WriteFile(source, []byte("changed"), 0600)
			if err := s.SyncAutomatic(t.Context(), p.ID); err != nil {
				t.Fatal(err)
			}
			after, _ := s.Read(p.ID)
			if (after.Latest != before.Latest) != (mode == "sync") {
				t.Fatalf("wrong AGENTS behavior for %s", mode)
			}
		})
	}
}

func TestPluginProjectionPreservesBundleAndLocalIdentity(t *testing.T) {
	s, p := fixture(t)
	source := filepath.Join(t.TempDir(), "sample")
	_ = os.MkdirAll(filepath.Join(source, ".codex-plugin"), 0700)
	_ = os.WriteFile(filepath.Join(source, ".codex-plugin", "plugin.json"), []byte(`{"name":"sample","version":"1.0.0"}`), 0600)
	_ = os.WriteFile(filepath.Join(source, ".mcp.json"), []byte(`{"mcpServers":{"fixture":{"command":"fixture"}}}`), 0600)
	p.Plugins = Selection{Mode: "projection", Allow: []Item{{ID: "sample@source", Path: source}}}
	status := create(t, s, p)
	pkg := status.Snapshots[0].Packages[0]
	if pkg.LocalID == pkg.ID || pkg.ID != "sample@source" {
		t.Fatal("source/local identity not separated")
	}
	root := filepath.Join(s.Root, p.ID, "generations", status.Latest, "plugins", "cache", "tend-"+digest([]byte(pkg.ID))[:16], "sample", "1.0.0")
	if _, err := os.Stat(filepath.Join(root, ".mcp.json")); err != nil {
		t.Fatal("plugin lost bundled MCP", err)
	}
	if _, err := s.Execute(t.Context(), Request{Action: "plugin remove", ID: p.ID, Item: Item{ID: pkg.ID}, RequestID: "remove", ExpectedRevision: status.Revision}); err == nil {
		t.Fatal("projected item accepted as independent installation")
	}
}

func TestIdleAdoptionFailureRestoresPreviousGeneration(t *testing.T) {
	s, p := fixture(t)
	source := skill(t, t.TempDir(), "selected", "v1")
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: source}}}
	create(t, s, p)
	b := &Binding{Directory: s.Root, ID: p.ID}
	original, err := b.Pin(t.Context(), "coordinator:test")
	if err != nil {
		t.Fatal(err)
	}
	skill(t, filepath.Dir(source), "selected", "v2")
	if err = s.SyncAutomatic(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	if original.Current() {
		t.Fatal("update not pending")
	}
	if err = original.Adopt(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := b.Pin(t.Context(), "coordinator:test")
	if err != nil || next.Generation == original.Generation || !next.ResumeOnly {
		t.Fatal("adoption not committed", err)
	}
	if err = next.Record(t.Context(), "initialized"); err != nil {
		t.Fatal(err)
	}
	if err = next.Record(t.Context(), "startup_failed"); err != nil {
		t.Fatal(err)
	}
	fallback, err := b.Pin(t.Context(), "coordinator:test")
	if err != nil || fallback.Generation != original.Generation || !fallback.Current() || !fallback.ResumeOnly {
		t.Fatal("fallback not stable", err)
	}
	if err = fallback.Record(t.Context(), "ready"); err != nil {
		t.Fatal(err)
	}
	status, _ := s.Read(p.ID)
	if _, err = s.Execute(t.Context(), Request{Action: "sync", ID: p.ID, RequestID: "explicit-retry", ExpectedRevision: status.Revision}); err != nil {
		t.Fatal(err)
	}
	if fallback.Current() {
		t.Fatal("explicit synchronization could not retry the blocked generation")
	}
}

func TestDisabledEnvironmentPreservesPinnedRecoveryWithoutAdopting(t *testing.T) {
	s, p := fixture(t)
	source := skill(t, t.TempDir(), "selected", "v1")
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: source}}}
	status := create(t, s, p)
	binding := &Binding{Directory: s.Root, ID: p.ID}
	ref, err := binding.Pin(t.Context(), "run:retained")
	if err != nil {
		t.Fatal(err)
	}
	skill(t, filepath.Dir(source), "selected", "v2")
	p.Enabled = false
	if _, err = s.Execute(t.Context(), Request{Action: "update", ID: p.ID, RequestID: "disable", ExpectedRevision: status.Revision, Profile: &p}); err != nil {
		t.Fatal(err)
	}
	if _, err = binding.Pin(t.Context(), "run:new"); err == nil {
		t.Fatal("disabled environment admitted a new run")
	}
	retained, err := binding.Pin(t.Context(), "run:retained")
	if err != nil || retained.Generation != ref.Generation || !ref.Current() {
		t.Fatal("disabling the environment changed pinned recovery", err)
	}
	if err = ref.Adopt(t.Context()); err == nil {
		t.Fatal("disabled environment adopted a different snapshot")
	}
}

func TestUnknownInitialProcessCannotBecomeStartupFailed(t *testing.T) {
	s, p := fixture(t)
	create(t, s, p)
	ref, err := (&Binding{Directory: s.Root, ID: p.ID}).Pin(t.Context(), "run:unknown-initial")
	if err != nil {
		t.Fatal(err)
	}
	if err = ref.Record(t.Context(), "unknown"); err != nil {
		t.Fatal(err)
	}
	if err = ref.Record(t.Context(), "startup_failed"); err == nil {
		t.Fatal("startup failure overwrote unconfirmed process closure")
	}
	status, err := s.Read(p.ID)
	if err != nil || status.Adoptions[ref.Execution].State != "unknown" {
		t.Fatal("unknown process became inactive", err)
	}
}
