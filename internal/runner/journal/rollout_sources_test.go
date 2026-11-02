package journal

import (
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRolloutLocatorSurvivesEventCleanupReopenAndRunCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	ctx := t.Context()
	as := protocol.Assignment{RunID: "run", Role: protocol.RoleImplement}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := j.RegisterRollout(ctx, "deployment", "run", protocol.RolloutPreviewSource{SessionID: id, TurnID: "turn", Path: filepath.Join(t.TempDir(), id)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.SetPhase(ctx, "run", PhaseDone); err != nil {
		t.Fatal(err)
	}
	if err := j.AckEvents(ctx, "run", 100); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	j = open(t, path)
	defer j.Close()
	sources, err := j.RolloutSources(ctx, "deployment", "run")
	if err != nil || len(sources) != 2 || sources[0].SessionID != "first" || sources[1].SessionID != "second" {
		t.Fatal(sources, err)
	}
	if err = j.RegisterRollout(ctx, "another-deployment", "run", sources[0]); err == nil {
		t.Fatal("cross-deployment adoption accepted")
	}
	if err = j.RegisterRollout(ctx, "another-deployment", "another-run", protocol.RolloutPreviewSource{SessionID: "first", TurnID: "different-turn"}); err == nil {
		t.Fatal("cross-deployment session adoption accepted with a different turn")
	}
	if err = j.RegisterRollout(ctx, "deployment", "another-run", sources[0]); err == nil {
		t.Fatal("cross-run turn adoption accepted")
	}
	if err = j.RegisterRollout(ctx, "deployment", "another-run", protocol.RolloutPreviewSource{SessionID: "first", TurnID: "new-turn"}); err != nil {
		t.Fatal("manager session reuse was rejected", err)
	}
}

func TestCleanupPreservesRegisteredOriginalAndArtifacts(t *testing.T) {
	root := t.TempDir()
	j := open(t, filepath.Join(root, "journal.db"))
	defer j.Close()
	path := filepath.Join(root, "native", "rollout-session.jsonl")
	if err := j.RegisterRollout(t.Context(), "deployment", "run", protocol.RolloutPreviewSource{SessionID: "session", Path: path}); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{root, filepath.Dir(path), path, filepath.Join(root, "native", "rollout-session.artifacts"), filepath.Join(root, "native", "rollout-session.artifacts", "tool-results")} {
		if err := j.ProtectRollouts(t.Context(), directory); err == nil {
			t.Fatal("cleanup accepted", directory)
		}
	}
	if err := j.ProtectRollouts(t.Context(), filepath.Join(root, "unrelated")); err != nil {
		t.Fatal(err)
	}
}
