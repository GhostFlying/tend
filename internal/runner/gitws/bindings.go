package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

var repositoryName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// RepositoryBinding contains local provider identity and a verified remote.
type RepositoryBinding = protocol.LocalRepositoryBinding

// BindRepositoryRequest is persisted under its identity before publication.
type BindRepositoryRequest = protocol.BindLocalRepositoryRequest

type bindingReceipt struct {
	Request  BindRepositoryRequest `json:"request"`
	Revision int64                 `json:"revision"`
}
type bindingRegistry struct {
	Revision int64                        `json:"revision"`
	Bindings map[string]RepositoryBinding `json:"bindings"`
	Requests map[string]bindingReceipt    `json:"requests"`
}

func (w *Workspaces) bindingsPath() string {
	return filepath.Join(w.cfg.Home, "repository-bindings.json")
}

func (w *Workspaces) repo(name string) Repo {
	w.owner().repoMu.RLock()
	defer w.owner().repoMu.RUnlock()
	r := w.cfg.Repos[name]
	if b, ok := w.owner().bindings.Bindings[name]; ok {
		r.Orca = b.Orca
	}
	return r
}

// LoadBindings validates persistent bindings before the runner advertises them.
func (w *Workspaces) LoadBindings(ctx context.Context) error {
	w.owner().bindMu.Lock()
	defer w.owner().bindMu.Unlock()
	raw, err := os.ReadFile(w.bindingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gitws: read repository bindings: %w", err)
	}
	var saved bindingRegistry
	if err := json.Unmarshal(raw, &saved); err != nil {
		return fmt.Errorf("gitws: decode repository bindings: %w", err)
	}
	for name, b := range saved.Bindings {
		if name != b.Repo {
			return errors.New("gitws: repository binding identity mismatch")
		}
		verified, err := w.validateBinding(ctx, b, false)
		if err != nil {
			return err
		}
		if verified != b {
			return errors.New("gitws: repository provider identity changed")
		}
	}
	if err := syncBindingsDirectory(w.bindingsPath()); err != nil {
		return fmt.Errorf("gitws: confirm binding persistence: %w", err)
	}
	w.owner().repoMu.Lock()
	defer w.owner().repoMu.Unlock()
	w.owner().bindings = saved
	return nil
}

func (w *Workspaces) validateBinding(ctx context.Context, b RepositoryBinding, checkStatic bool) (RepositoryBinding, error) {
	if !repositoryName.MatchString(b.Repo) || (b.RemoteURL == "" || strings.TrimSpace(b.RemoteURL) != b.RemoteURL) || strings.HasPrefix(b.RemoteURL, "-") || strings.ContainsAny(b.RemoteURL, "\r\n\x00") {
		return b, errors.New("gitws: a repository name and remote URL are required")
	}
	bindingLayout := w.layout
	if b.Orca != "" {
		if named := w.owner().backends["orca"]; named != nil {
			bindingLayout = named.layout
		}
	}
	if l, ok := bindingLayout.(orcaLayout); ok {
		if b.Orca == "" {
			return b, errors.New("gitws: an existing Orca repository selector is required")
		}
		repo, err := l.orca.Repo(ctx, b.Orca)
		if err != nil {
			return b, fmt.Errorf("gitws: resolve existing Orca repository: %w", err)
		}
		remote, err := w.git.run(ctx, repo.Path, "remote", "get-url", "origin")
		if err != nil {
			return b, fmt.Errorf("gitws: read repository origin: %w", err)
		}
		if repo.ID == "" || protocol.RepositoryRemoteKey(remote) != protocol.RepositoryRemoteKey(b.RemoteURL) {
			return b, errors.New("gitws: Orca repository origin does not match the requested remote")
		}
		// An existing static selector may have another spelling of the same ID.
		if old := w.cfg.Repos[b.Repo].Orca; checkStatic && old != "" {
			current, err := l.orca.Repo(ctx, old)
			if err != nil || current.ID != repo.ID {
				return b, errors.New("gitws: an existing repository binding cannot be replaced")
			}
		}
		b.Orca = "id:" + repo.ID
	} else if b.Orca != "" {
		return b, errors.New("gitws: this runner does not use Orca")
	}
	return b, nil
}

// BindRepository only adds bindings. Existing workspaces never change identity.
// Exact request replay returns its original revision even after later additions.
func (w *Workspaces) BindRepository(ctx context.Context, request BindRepositoryRequest) (int64, error) {
	if !protocol.ValidID(protocol.PrefixRequest, request.RequestID) {
		return 0, errors.New("gitws: a valid request_id is required")
	}
	// Serialize writers independently of the read path used by live heartbeats.
	w.owner().bindMu.Lock()
	defer w.owner().bindMu.Unlock()
	if w.owner().bindingsPending != nil {
		if err := syncBindingsDirectory(w.bindingsPath()); err != nil {
			return 0, fmt.Errorf("gitws: binding persistence remains uncertain: %w", err)
		}
		w.owner().repoMu.Lock()
		w.owner().bindings = *w.owner().bindingsPending
		w.owner().repoMu.Unlock()
		w.owner().bindingsPending = nil
	}
	w.owner().repoMu.RLock()
	current := w.owner().bindings
	w.owner().repoMu.RUnlock()
	if receipt, ok := current.Requests[request.RequestID]; ok {
		if receipt.Request != request {
			return 0, errors.New("gitws: request identity conflicts with recorded binding")
		}
		return receipt.Revision, nil
	}
	if request.ExpectedRevision != current.Revision {
		return 0, fmt.Errorf("gitws: repository registry revision is %d", current.Revision)
	}
	_, alreadyBound := current.Bindings[request.Binding.Repo]
	b, err := w.validateBinding(ctx, request.Binding, !alreadyBound)
	if err != nil {
		return 0, err
	}
	if old, ok := current.Bindings[b.Repo]; ok && (old.Orca != b.Orca || protocol.RepositoryRemoteKey(old.RemoteURL) != protocol.RepositoryRemoteKey(b.RemoteURL)) {
		return 0, errors.New("gitws: an existing repository binding cannot be replaced")
	}
	next := bindingRegistry{Revision: current.Revision + 1, Bindings: map[string]RepositoryBinding{}, Requests: map[string]bindingReceipt{}}
	for name, b := range current.Bindings {
		next.Bindings[name] = b
	}
	for id, receipt := range current.Requests {
		next.Requests[id] = receipt
	}
	next.Bindings[b.Repo] = b
	next.Requests[request.RequestID] = bindingReceipt{Request: request, Revision: next.Revision}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return 0, err
	}
	if published, err := persistBindings(w.bindingsPath(), append(raw, '\n')); err != nil {
		if published {
			w.owner().bindingsPending = &next
			return 0, fmt.Errorf("gitws: binding outcome unknown; reconcile the original request: %w", err)
		}
		return 0, err
	}
	w.owner().repoMu.Lock()
	w.owner().bindings = next
	w.owner().repoMu.Unlock()
	return next.Revision, nil
}

func persistBindings(path string, raw []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("gitws: create binding directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".repository-bindings-*")
	if err != nil {
		return false, fmt.Errorf("gitws: stage bindings: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return false, err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return false, fmt.Errorf("gitws: publish bindings: %w", err)
	}
	return true, syncBindingsDirectory(path)
}

func syncBindingsDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// RepositoryBindings returns a detached observation, with no local identities.
func (w *Workspaces) RepositoryBindings() []protocol.RunnerRepository {
	w.owner().repoMu.RLock()
	defer w.owner().repoMu.RUnlock()
	names := make([]string, 0, len(w.owner().bindings.Bindings))
	for name := range w.owner().bindings.Bindings {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]protocol.RunnerRepository, 0, len(names))
	for _, name := range names {
		b := w.owner().bindings.Bindings[name]
		out = append(out, protocol.RunnerRepository{Repo: name, RemoteIdentity: protocol.RepositoryRemoteKey(b.RemoteURL)})
	}
	return out
}

// BindingRevision is the local catalog's optimistic control revision.
func (w *Workspaces) BindingRevision() int64 {
	w.owner().repoMu.RLock()
	defer w.owner().repoMu.RUnlock()
	return w.owner().bindings.Revision
}
