package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	runnerpkg "github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/store"
)

// Exercise the real per-Run baseline instead of inventing Changed=true for a
// Run that only reports an installation. Git commits here are isolated fixtures.
func predeploymentGitWorkspace(t *testing.T, as protocol.Assignment) (*gitws.Workspaces, protocol.Assignment, protocol.ArtifactPayload) {
	t.Helper()
	dir := t.TempDir()
	config := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(config, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"GIT_CONFIG_GLOBAL=" + config, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com"}
	git := func(at string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", at}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	seed, remote := filepath.Join(dir, "seed"), filepath.Join(dir, "remote.git")
	git(dir, "init", "--quiet", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(seed, "add", "README")
	git(seed, "commit", "--quiet", "-m", "base fixture")
	git(dir, "clone", "--quiet", "--bare", seed, remote)
	ws := gitws.New(gitws.Config{Home: filepath.Join(dir, "worker"), Env: env})
	as.Workspace = protocol.AssignmentWorkspace{ID: "ws_predeployment", Name: "predeployment", Branch: "tend/predeployment", Mounts: []protocol.AssignedMount{{Repo: "tend", Path: "tend", RemoteURL: remote}}}
	as.MountDirectories = true
	p, err := ws.Prepare(t.Context(), as)
	if err != nil || len(p.Report.Mounts) != 1 || p.Report.Mounts[0].Directory == "" {
		t.Fatal("real workspace preparation", p, err)
	}
	repo := p.Report.Mounts[0].Directory
	if err := os.WriteFile(filepath.Join(repo, "candidate"), []byte("reviewed candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "candidate")
	git(repo, "commit", "--quiet", "-m", "candidate fixture")
	artifact, err := ws.Collect(t.Context(), as)
	if err != nil || len(artifact.Mounts) != 1 || !artifact.Mounts[0].Changed {
		t.Fatal("real candidate collection", artifact, err)
	}
	return ws, as, artifact
}

func unchangedInstallationWorkspace(t *testing.T, ws *gitws.Workspaces, prepared protocol.Assignment, as protocol.Assignment) protocol.ArtifactPayload {
	t.Helper()
	// Keep the actual repair Run identity/spec; only the external Git fixture
	// resource replaces the database-only test workspace location.
	as.Workspace = prepared.Workspace
	as.MountDirectories = true
	if _, err := ws.Prepare(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Collect(t.Context(), as); !errors.Is(err, runnerpkg.ErrNoChanges) {
		t.Fatal("installation fabricated source changes", err)
	}
	state, err := ws.Snapshot(t.Context(), as)
	if err != nil || len(state.Mounts) != 1 || state.Mounts[0].Changed || state.Mounts[0].SnapshotSHA != "" {
		t.Fatal("real unchanged commit required", state, err)
	}
	return state
}

func TestPredeploymentUnchangedStateDoesNotRelaxOrdinaryCommitOutput(t *testing.T) {
	h, _, _, as, _, stage := predeploymentFixture(t)
	stage.Mounts[0].Changed = false
	ordinary := stage
	ordinary.Report = &protocol.ResultReport{Body: "An unchanged ordinary commit result", Evidence: []string{"upgrade:invented"}}
	expectWorkCode(t, h.runAs(as, protocol.OpRunsSubmitArtifact, ordinary, SubmitArtifact{}), protocol.CodeInvalidRequest)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal("real typed stage must not invent changes", op)
	}
	artifactID := h.task(as.TaskID).CurrentArtifactID
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, err := store.GetArtifact(t.Context(), tx.Q(), artifactID)
		if err != nil {
			return err
		}
		if strings.Contains(a.RefJSON, `"changed":true`) {
			t.Fatal("stored stage fabricated a Changed flag")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
