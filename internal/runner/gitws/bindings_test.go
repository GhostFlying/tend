package gitws

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func bindingRequest(suffix byte, revision int64, b RepositoryBinding) BindRepositoryRequest {
	return BindRepositoryRequest{RequestID: "req_01ARZ3NDEKTSV4RRFFQ69G5FA" + string(suffix), ExpectedRevision: revision, Binding: b}
}

func TestBindRepositoryKeepsExistingDirtyWorkspaceAndPersists(t *testing.T) {
	f, o := newOrcaFixture(t)
	ctx := context.Background()
	as := f.implement()
	before := f.prepare(as)
	write(t, filepath.Join(before.Dir, "dirty.txt"), "owner changes\n")
	bad := bindingRequest('A', 0, RepositoryBinding{Repo: "added", RemoteURL: "https://wrong.example/repo.git", Orca: "name:tend"})
	if _, err := f.ws.BindRepository(ctx, bad); err == nil {
		t.Fatal("wrong origin accepted")
	}
	if len(f.ws.RepositoryBindings()) != 0 {
		t.Fatal("failed validation published a binding")
	}
	request := bindingRequest('B', 0, RepositoryBinding{Repo: "added", RemoteURL: f.url, Orca: "name:tend"})
	if revision, err := f.ws.BindRepository(ctx, request); err != nil || revision != 1 {
		t.Fatal(revision, err)
	}
	if f.ws.repo("added").Orca != "id:repo-1" {
		t.Fatal("provider ID not pinned")
	}
	if _, err := f.ws.BindRepository(ctx, request); err != nil {
		t.Fatal("request replay", err)
	}
	conflict := request
	conflict.Binding.RemoteURL = "https://wrong.example/repo.git"
	if _, err := f.ws.BindRepository(ctx, conflict); err == nil {
		t.Fatal("reused request changed payload")
	}
	stale := bindingRequest('C', 0, RepositoryBinding{Repo: "another", RemoteURL: f.url, Orca: "name:tend"})
	if _, err := f.ws.BindRepository(ctx, stale); err == nil {
		t.Fatal("stale write accepted")
	}
	after := f.prepare(as)
	if after.Dir != before.Dir || o.creates != 1 {
		t.Fatal("existing workspace changed")
	}
	if got, err := os.ReadFile(filepath.Join(after.Dir, "dirty.txt")); err != nil || string(got) != "owner changes\n" {
		t.Fatal("dirty files changed", err)
	}
	saved := f.ws.bindings
	f.ws.bindings = bindingRegistry{}
	if err := f.ws.LoadBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if f.ws.BindingRevision() != saved.Revision || len(f.ws.RepositoryBindings()) != 1 {
		t.Fatal("restart lost binding")
	}
	if _, err := f.ws.BindRepository(ctx, request); err != nil {
		t.Fatal("durable request receipt lost", err)
	}
	info, err := os.Stat(f.ws.bindingsPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("binding registry is not private", err)
	}
}

func TestFault_BindingPersistenceFailureDoesNotAdvertise(t *testing.T) {
	f := newFixture(t, nil)
	if err := os.MkdirAll(f.ws.bindingsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	request := bindingRequest('D', 0, RepositoryBinding{Repo: "new", RemoteURL: f.url})
	if _, err := f.ws.BindRepository(context.Background(), request); err == nil {
		t.Fatal("persist failure accepted")
	}
	if f.ws.BindingRevision() != 0 || len(f.ws.RepositoryBindings()) != 0 {
		t.Fatal("uncommitted binding became visible")
	}
}

func TestBindingPublicationConcurrentWithWorkspaceReads(t *testing.T) {
	f := newFixture(t, nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 1000 {
				_ = f.ws.repo("new")
				_ = f.ws.RepositoryBindings()
			}
		})
	}
	if _, err := f.ws.BindRepository(context.Background(), bindingRequest('E', 0, RepositoryBinding{Repo: "new", RemoteURL: f.url})); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if len(f.ws.RepositoryBindings()) != 1 {
		t.Fatal("publication lost")
	}
	// A changed name or remote must not replace an already published identity.
	if _, err := f.ws.BindRepository(context.Background(), bindingRequest('F', 1, RepositoryBinding{Repo: "new", RemoteURL: "https://wrong.example/new.git"})); err == nil {
		t.Fatal("existing repository replaced")
	}
}

func TestBindingRejectsDifferentLocalRepository(t *testing.T) {
	f, _ := newOrcaFixture(t)
	other := strings.TrimSuffix(f.url, ".git")
	run(t, f.env, filepath.Dir(other), "init", "--bare", "--quiet", other)
	request := bindingRequest('G', 0, RepositoryBinding{Repo: "new", RemoteURL: other, Orca: "name:tend"})
	if _, err := f.ws.BindRepository(t.Context(), request); err == nil {
		t.Fatal("binding accepted a different local remote merely because its path differed by .git")
	}
}

func TestPinnedBindingSurvivesBootstrapSelectorRename(t *testing.T) {
	f, o := newOrcaFixture(t)
	if _, err := f.ws.BindRepository(t.Context(), bindingRequest('H', 0, RepositoryBinding{Repo: "tend", RemoteURL: f.url, Orca: "name:tend"})); err != nil {
		t.Fatal(err)
	}
	layout := f.ws.layout.(orcaLayout)
	layout.orca.Exec = func(ctx context.Context, dir string, args []string) ([]byte, error) {
		if args[0] == "repo" && args[1] == "show" && args[3] == "name:tend" {
			return []byte(`{"ok":false,"error":{"code":"not_found","message":"repository renamed"}}`), nil
		}
		return o.exec(ctx, dir, args)
	}
	if err := f.ws.LoadBindings(t.Context()); err != nil {
		t.Fatal("bootstrap selector overrode the durable provider ID", err)
	}
	if observed := f.ws.RepositoryBindings(); len(observed) != 1 || observed[0].RemoteIdentity != protocol.RepositoryRemoteKey(f.url) {
		t.Fatal("pinned binding lost", observed)
	}
	if _, err := f.ws.BindRepository(t.Context(), bindingRequest('J', 1, RepositoryBinding{Repo: "tend", RemoteURL: f.url, Orca: "id:repo-1"})); err != nil {
		t.Fatal("same identity could not be confirmed", err)
	}
}
