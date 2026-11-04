package gitws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

// Reattach reads an original workspace without repeating preparation effects.
// In particular, a missing state, checkout or scratch directory is not repaired.
func (w *Workspaces) Reattach(ctx context.Context, as protocol.Assignment) (runner.Prepared, error) {
	if as.Workspace.BackendID != "" && as.Workspace.BackendID != w.backendID {
		view, err := w.Backend(as.Workspace.BackendID)
		if err != nil {
			return runner.Prepared{}, err
		}
		return view.Reattach(ctx, as)
	}
	if _, ok := w.layout.(orcaLayout); !ok {
		return runner.Prepared{}, errors.New("gitws: strict attachment requires the original Orca workspace backend")
	}
	home := w.home(as)
	unlock, err := w.lock(ctx, "ws:"+home)
	if err != nil {
		return runner.Prepared{}, err
	}
	defer unlock()
	if _, err := os.Stat(statePath(home)); err != nil {
		return runner.Prepared{}, fmt.Errorf("gitws: original workspace state is unavailable: %w", err)
	}
	st, err := loadState(home)
	if err != nil {
		return runner.Prepared{}, err
	}
	if as.Workspace.ID == "" || st.ResourceID != as.Workspace.ID || st.BackendID != as.Workspace.BackendID || st.ProviderID == "" {
		return runner.Prepared{}, errors.New("gitws: original workspace resource identity differs")
	}
	if as.Workspace.ProviderID != "" && as.Workspace.ProviderID != st.ProviderID || as.Workspace.ProviderInstanceID != "" && as.Workspace.ProviderInstanceID != st.ProviderInstanceID {
		return runner.Prepared{}, errors.New("gitws: original workspace provider identity differs")
	}
	root := w.layout.root(home, st)
	scratch := filepath.Join(w.cfg.Home, "scratch", as.Workspace.Name)
	if as.Workspace.Kind == "directory" {
		root = home
		scratch = filepath.Join(w.cfg.Home, "scratch", as.Workspace.ID)
	}
	if as.Role == protocol.RoleAccept {
		scratch = filepath.Join(w.cfg.Home, "scratch", ".accept", as.RunID)
	}
	if as.Workspace.Path != "" && filepath.Clean(as.Workspace.Path) != filepath.Clean(root) {
		return runner.Prepared{}, errors.New("gitws: original workspace path differs")
	}
	p := runner.Prepared{Dir: root, ScratchDir: scratch, StateDir: filepath.Join(w.cfg.Home, "runs", as.RunID), ProviderID: st.ProviderID, Guide: repoGuide(as, st)}
	if len(st.Mounts) != len(as.Workspace.Mounts) {
		return runner.Prepared{}, errors.New("gitws: original workspace mount set differs")
	}
	for _, m := range as.Workspace.Mounts {
		ms, ok := st.Mounts[m.Path]
		if !ok || ms.Repo != m.Repo || ms.Base == "" || ms.ProviderID != st.ProviderID || ms.ProviderInstanceID != st.ProviderInstanceID {
			return runner.Prepared{}, errors.New("gitws: original workspace mount identity is unavailable")
		}
		if err := retainedDirectory(ms.dir(home, m.Path)); err != nil {
			return runner.Prepared{}, err
		}
		p.Baseline = append(p.Baseline, harness.MountBaseline{Path: m.Path, StartHEAD: taskBase(as, ms), ResourceBase: ms.Base})
		p.Report.Mounts = append(p.Report.Mounts, protocol.ResolvedMount{Path: m.Path, Directory: ms.dir(home, m.Path), ResolvedBaseSHA: ms.Base, ResolvedFrom: ms.From, Branch: ms.Branch})
	}
	for _, dir := range []string{p.Dir, p.StateDir, p.ScratchDir} {
		if err := retainedDirectory(dir); err != nil {
			return runner.Prepared{}, err
		}
	}
	p.Report.BackendID, p.Report.ProviderID, p.Report.ProviderInstanceID, p.Report.Path = st.BackendID, st.ProviderID, st.ProviderInstanceID, root
	return p, nil
}

func retainedDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("gitws: original workspace directory is unavailable: %w", err)
	}
	if !info.IsDir() {
		return errors.New("gitws: original workspace directory was replaced")
	}
	return nil
}
