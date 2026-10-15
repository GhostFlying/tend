package gitws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
)

// runProfiles runs the prepare profiles of each mount and then of the
// workspace (D35). A profile whose inputs are unchanged since it last
// succeeded in this workspace is skipped.
func (w *Workspaces) runProfiles(ctx context.Context, as protocol.Assignment, root string, st *state) error {
	type step struct {
		key, dir string
		p        Profile
	}
	var steps []step
	for _, m := range as.Workspace.Mounts {
		if m.Profile == "" {
			continue
		}
		p, ok := w.cfg.Repos[m.Repo].Profiles[m.Profile]
		if !ok {
			return fmt.Errorf("workspace_prepare_failed: repo %s has no profile %q on this runner", m.Repo, m.Profile)
		}
		steps = append(steps, step{"mount:" + m.Path + ":" + m.Profile, filepath.Join(root, m.Path), p})
	}
	if name := as.Workspace.Profile; name != "" {
		p, ok := w.cfg.Profiles[name]
		if !ok {
			return fmt.Errorf("workspace_prepare_failed: this runner has no workspace profile %q", name)
		}
		steps = append(steps, step{"workspace:" + name, root, p})
	}
	for _, s := range steps {
		hash, err := inputsHash(s.dir, s.p)
		if err != nil {
			return err
		}
		if prev, ok := st.Prepared[s.key]; ok && prev == hash {
			continue
		}
		if err := w.runProfile(ctx, s.dir, s.p); err != nil {
			return fmt.Errorf("profile %s: %w", s.key, err)
		}
		st.Prepared[s.key] = hash
	}
	return nil
}

func (w *Workspaces) runProfile(ctx context.Context, dir string, p Profile) error {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, argv := range p.Commands {
		if len(argv) == 0 {
			continue
		}
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), w.cfg.Env...)
		procx.Contain(c)
		out, err := c.CombinedOutput()
		if err != nil {
			tail := string(out)
			if len(tail) > 4000 {
				tail = "…" + tail[len(tail)-4000:]
			}
			return fmt.Errorf("%s: %w: %s", redact(strings.Join(argv, " ")), err, redact(tail))
		}
		if w.cfg.Log != nil {
			w.cfg.Log.InfoContext(ctx, "prepare step done", "dir", dir, "command", strings.Join(argv, " "))
		}
	}
	return nil
}

// inputsHash hashes the files a profile depends on. With no declared
// inputs the hash is constant, so the profile runs once per workspace.
func inputsHash(dir string, p Profile) (string, error) {
	h := sha256.New()
	for _, c := range p.Commands {
		fmt.Fprintf(h, "cmd %q\n", c)
	}
	var files []string
	for _, pattern := range p.RerunIfChanged {
		if base, ok := strings.CutSuffix(pattern, "/**"); ok {
			err := filepath.WalkDir(filepath.Join(dir, base), func(path string, d fs.DirEntry, err error) error {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				if err == nil && !d.IsDir() {
					files = append(files, path)
				}
				return err
			})
			if err != nil {
				return "", fmt.Errorf("gitws: walk %s: %w", pattern, err)
			}
			continue
		}
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return "", fmt.Errorf("gitws: pattern %s: %w", pattern, err)
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("gitws: read %s: %w", f, err)
		}
		rel, _ := filepath.Rel(dir, f)
		fmt.Fprintf(h, "file %s %d\n", rel, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
