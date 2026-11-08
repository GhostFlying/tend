//go:build linux

package codexenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionBindingSurvivesMappingReplacementAndRemoval(t *testing.T) {
	state := t.TempDir()
	original := &Binding{Directory: filepath.Join(t.TempDir(), "first"), ID: "original"}
	pinned, err := BindExecution(t.Context(), state, "run:original", original, false)
	if err != nil || pinned == nil || *pinned != *original {
		t.Fatal(pinned, err)
	}
	for _, changed := range []*Binding{{Directory: filepath.Join(t.TempDir(), "second"), ID: "replacement"}, nil} {
		restored, err := BindExecution(t.Context(), state, "run:original", changed, true)
		if err != nil || restored == nil || *restored != *original {
			t.Fatal("mapping redirected original run", restored, err)
		}
	}
	if _, err = BindExecution(t.Context(), state, "run:other", original, true); err == nil {
		t.Fatal("different run reused binding")
	}
	fresh, err := BindExecution(t.Context(), t.TempDir(), "run:new", &Binding{Directory: original.Directory, ID: "replacement"}, false)
	if err != nil || fresh.ID != "replacement" {
		t.Fatal(fresh, err)
	}
}

func TestLegacyThreadDoesNotAdoptNewEnvironmentMapping(t *testing.T) {
	state := t.TempDir()
	result, err := BindExecution(t.Context(), state, "run:legacy", &Binding{Directory: t.TempDir(), ID: "new"}, true)
	if err != nil || result != nil {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(filepath.Join(state, "codex-environment.json")); !os.IsNotExist(err) {
		t.Fatal("legacy execution acquired a new binding", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = BindExecution(ctx, state, "run:legacy", nil, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDisablingMissingProjectionRetainsSnapshotAndRecovery(t *testing.T) {
	s, p := fixture(t)
	source := skill(t, t.TempDir(), "selected", "v1")
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: source}}}
	status := create(t, s, p)
	binding := &Binding{Directory: s.Root, ID: p.ID}
	original, err := binding.Pin(t.Context(), "run:retained")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	disabled := status.Profile
	disabled.Enabled = false
	preview, err := s.Execute(t.Context(), Request{Action: "update", ID: p.ID, DryRun: true, Profile: &disabled})
	if err != nil || !preview.Changed {
		t.Fatal(preview, err)
	}
	result, err := s.Execute(t.Context(), Request{Action: "update", ID: p.ID, RequestID: "disable", ExpectedRevision: status.Revision, Profile: &disabled})
	if err != nil || result.Status.Profile.Enabled || result.Status.Latest != status.Latest || result.Status.Revision != status.Revision+1 {
		t.Fatal(result, err)
	}
	if _, err = binding.Pin(t.Context(), "run:new"); err == nil {
		t.Fatal("disabled environment admitted new execution")
	}
	restored, err := binding.Pin(t.Context(), "run:retained")
	if err != nil || restored.Generation != original.Generation {
		t.Fatal(restored, err)
	}
	replay, err := s.Execute(t.Context(), Request{Action: "reconcile", ID: p.ID, RequestID: "disable"})
	if err != nil || replay.Status.Revision != result.Status.Revision {
		t.Fatal(replay, err)
	}
	disabled.Enabled = true
	if _, err = s.Execute(t.Context(), Request{Action: "update", ID: p.ID, RequestID: "enable", ExpectedRevision: result.Status.Revision, Profile: &disabled}); err == nil {
		t.Fatal("enabling accepted missing source")
	}
}

func TestDisabledProfileCanRestoreDeletedEnvironment(t *testing.T) {
	s, p := fixture(t)
	p.Enabled = false
	status := create(t, s, p)
	deleted, err := s.Execute(t.Context(), Request{Action: "delete", ID: p.ID, RequestID: "delete", ExpectedRevision: status.Revision})
	if err != nil {
		t.Fatal(err)
	}
	profile := deleted.Status.Profile
	restored, err := s.Execute(t.Context(), Request{Action: "update", ID: p.ID, RequestID: "restore", ExpectedRevision: deleted.Status.Revision, Profile: &profile})
	if err != nil || restored.Status.Deleted || restored.Status.Profile.Enabled {
		t.Fatal(restored, err)
	}
}

func TestQuestionContinuationInheritsOriginalEnvironmentAcrossRuns(t *testing.T) {
	previous, state := t.TempDir(), t.TempDir()
	original := &Binding{Directory: t.TempDir(), ID: "original"}
	if _, err := BindExecution(t.Context(), previous, "run:before", original, false); err != nil {
		t.Fatal(err)
	}
	restored, err := InheritExecution(t.Context(), state, "run:after", previous, "run:before")
	if err != nil || restored == nil || *restored != *original {
		t.Fatal(restored, err)
	}
	if err = os.RemoveAll(previous); err != nil {
		t.Fatal(err)
	}
	pinned, err := InheritExecution(t.Context(), state, "run:after", previous, "run:before")
	if err != nil || pinned == nil || *pinned != *original {
		t.Fatal("continuation lost its own saved identity", pinned, err)
	}
	legacy, err := InheritExecution(t.Context(), t.TempDir(), "run:legacy-after", t.TempDir(), "run:legacy-before")
	if err != nil || legacy != nil {
		t.Fatal("legacy continuation acquired isolation", legacy, err)
	}
}

func TestRecordedHomesAreIndependentOfMappingsAndRunHistory(t *testing.T) {
	s, p := fixture(t)
	create(t, s, p)
	registry := filepath.Join(t.TempDir(), "homes")
	binding := &Binding{Directory: s.Root, ID: p.ID}
	for range 2 {
		if err := RecordSessionHome(t.Context(), registry, binding); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := RecordedSessionRoots(registry)
	if err != nil || len(roots) != 1 || roots[0] != filepath.Join(p.Home, "sessions") {
		t.Fatal(roots, err)
	}
	if err = os.RemoveAll(s.Root); err != nil {
		t.Fatal(err)
	}
	roots, err = RecordedSessionRoots(registry)
	if err != nil || len(roots) != 1 || roots[0] != filepath.Join(p.Home, "sessions") {
		t.Fatal("lost original home after unmapping", roots, err)
	}
}
