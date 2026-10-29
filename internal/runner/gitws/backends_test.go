package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNamedBackendSharesLocksBindingsAndPreservesPhysicalWorkspace(t *testing.T) {
	f, o := newOrcaFixture(t)
	// Keep legacy git layout while adding the independently configured named Orca backend.
	root := New(Config{Home: f.ws.cfg.Home, Env: f.env, Repos: map[string]Repo{"tend": {Orca: "name:tend"}}})
	if err := root.AddBackend("orca", &orca.Client{Exec: o.exec}); err != nil {
		t.Fatal(err)
	}
	view, err := root.Backend("orca")
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := root.lock(t.Context(), "repository:shared")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := view.lock(ctx, "repository:shared"); !errors.Is(err, context.Canceled) {
		t.Fatal("backend has an independent host lock", err)
	}
	unlock()
	// The actual local binding command publishes one authority for both views.
	request := bindingRequest('J', 0, RepositoryBinding{Repo: "tend", RemoteURL: f.url, Orca: "name:tend"})
	if _, err := root.BindRepository(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if got := view.repo("tend").Orca; got != "id:repo-1" {
		t.Fatal("binding truth split", got)
	}
	if root.BindingRevision() != 1 || view.BindingRevision() != 1 {
		t.Fatal("binding receipt truth split")
	}
	if err := root.LoadBindings(t.Context()); err != nil {
		t.Fatal("named binding cannot restart", err)
	}

	f.ws = root
	as := f.implement()
	as.Workspace.ID = "wsp_original"
	as.Workspace.BackendID = "orca"
	p := f.prepare(as)
	if p.Report.BackendID != "orca" || p.Report.ProviderID == "" || p.Dir != filepath.Join(o.dir, "feature_a") || p.ProviderID == "" {
		t.Fatalf("actual provider metadata lost: %+v", p)
	}
	write(t, filepath.Join(p.Dir, "named.txt"), "implementation change\n")
	artifact, err := root.Collect(t.Context(), as)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	acc := as
	acc.Role = protocol.RoleAccept
	acc.RunID = "run_review"
	acc.Artifact = body
	acc.Workspace.Path = p.Dir
	acc.Workspace.ProviderID = p.Report.ProviderID
	review, err := root.Prepare(t.Context(), acc)
	if err != nil || review.Dir != p.Dir || o.creates != 1 {
		t.Fatal("acceptance moved shared workspace", review, err, o.creates)
	}
	// A changed bootstrap never migrates an existing physical workspace to git.
	if err := root.AddBackend("git", nil); err != nil {
		t.Fatal(err)
	}
	wrong := as
	wrong.Workspace.BackendID = "git"
	if _, err := root.Prepare(t.Context(), wrong); err == nil {
		t.Fatal("changed backend reused an existing workspace")
	}
	st, err := loadState(root.home(as))
	if err != nil || st.BackendID != "orca" {
		t.Fatal("original backend was rewritten", st, err)
	}
}
func TestBackendMismatchDoesNotRelabelAnotherResource(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ws.AddBackend("git", nil); err != nil {
		t.Fatal(err)
	}
	as := f.implement()
	as.Workspace.ID = "wsp_original"
	home := f.ws.home(as)
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveState(home, state{ResourceID: "wsp_other", Mounts: map[string]mountState{}}); err != nil {
		t.Fatal(err)
	}
	as.Workspace.BackendID = "git"
	if _, err := f.ws.Prepare(t.Context(), as); err == nil {
		t.Fatal("another resource accepted")
	}
	st, err := loadState(home)
	if err != nil || st.BackendID != "" || st.ResourceID != "wsp_other" {
		t.Fatal("mismatched resource was relabeled", st, err)
	}
}
