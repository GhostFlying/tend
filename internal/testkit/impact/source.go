package impact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Source binds a plan to the compared revisions and actual worktree contents.
type Source struct {
	BaseRef   string `json:"base_ref"`
	BaseTip   string `json:"base_tip"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	Tree      string `json:"tree"`
	Worktree  bool   `json:"worktree"`
	Digest    string `json:"digest"`
	Toolchain string `json:"toolchain"`
}

// Options selects an immutable head or explicitly includes local edits.
type Options struct {
	Base, Head, Go string
	Worktree, Full bool
}

func command(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func snapshot(ctx context.Context, root string, o Options) (Source, []string, error) {
	if o.Head == "" {
		o.Head = "HEAD"
	}
	if o.Base == "" {
		o.Base = "origin/main"
	}
	resolve := func(ref string) (string, error) {
		out, err := command(ctx, root, "git", "rev-parse", "--verify", ref+"^{commit}")
		return strings.TrimSpace(string(out)), err
	}
	head, err := resolve(o.Head)
	if err != nil {
		return Source{}, nil, err
	}
	current, err := resolve("HEAD")
	if err != nil {
		return Source{}, nil, err
	}
	if head != current {
		return Source{}, nil, errors.New("validation head must be checked out")
	}
	if !o.Worktree {
		dirty, err := command(ctx, root, "git", "status", "--porcelain", "--untracked-files=normal")
		if err != nil {
			return Source{}, nil, err
		}
		if len(dirty) != 0 {
			return Source{}, nil, errors.New("immutable validation plan requires a clean checkout; use explicit worktree mode for local edits")
		}
	}
	tip, err := resolve(o.Base)
	if err != nil {
		return Source{}, nil, err
	}
	baseBytes, err := command(ctx, root, "git", "merge-base", tip, head)
	if err != nil {
		return Source{}, nil, err
	}
	base := strings.TrimSpace(string(baseBytes))
	tree, err := command(ctx, root, "git", "rev-parse", head+"^{tree}")
	if err != nil {
		return Source{}, nil, err
	}
	args := []string{"git", "diff", "--no-renames", "--name-only", "-z", base}
	diffArgs := []string{"git", "diff", "--no-renames", "--binary", base}
	if !o.Worktree {
		args = append(args, head)
		diffArgs = append(diffArgs, head)
	}
	changed, err := command(ctx, root, args...)
	if err != nil {
		return Source{}, nil, err
	}
	diff, err := command(ctx, root, diffArgs...)
	if err != nil {
		return Source{}, nil, err
	}
	files := strings.Split(strings.TrimSuffix(string(changed), "\x00"), "\x00")
	if len(changed) == 0 {
		files = nil
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "%s\n%s\n%s\n%s\n", tip, base, head, runtime.Version())
	_, _ = digest.Write(diff)
	if o.Worktree {
		untracked, err := command(ctx, root, "git", "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return Source{}, nil, err
		}
		for _, file := range strings.Split(strings.TrimSuffix(string(untracked), "\x00"), "\x00") {
			if file == "" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, file))
			if err != nil {
				return Source{}, nil, err
			}
			files = append(files, file)
			fmt.Fprintf(digest, "\x00%s\x00", file)
			_, _ = digest.Write(data)
		}
	}
	// Package concurrency is assigned later by the shared check budget. Other
	// build flags (notably tags) affect the dependency graph and must match.
	var buildFlags []string
	for _, word := range strings.Fields(os.Getenv("GOFLAGS")) {
		if !strings.HasPrefix(word, "-p=") && !strings.HasPrefix(word, "--p=") {
			buildFlags = append(buildFlags, word)
		}
	}
	fmt.Fprintf(digest, "\nGOFLAGS=%s", strings.Join(buildFlags, " "))
	for _, key := range []string{"GOOS", "GOARCH", "CGO_ENABLED", "GOEXPERIMENT", "GOTOOLCHAIN"} {
		fmt.Fprintf(digest, "\n%s=%s", key, os.Getenv(key))
	}
	slices.Sort(files)
	files = slices.Compact(files)
	return Source{BaseRef: o.Base, BaseTip: tip, Base: base, Head: head, Tree: strings.TrimSpace(string(tree)), Worktree: o.Worktree, Digest: hex.EncodeToString(digest.Sum(nil)), Toolchain: runtime.Version()}, files, nil
}

// Load discovers current production and test import edges only when needed.
func Load(ctx context.Context, root string, o Options) (Plan, error) {
	source, files, err := snapshot(ctx, root, o)
	if err != nil {
		return Plan{}, err
	}
	var packages []Package
	needGraph := false
	for _, file := range files {
		needGraph = needGraph || strings.HasSuffix(file, ".go")
	}
	if needGraph && !o.Full {
		goTool := o.Go
		if goTool == "" {
			goTool = "go"
		}
		data, graphErr := command(ctx, root, goTool, "list", "-json", "./...")
		if graphErr != nil {
			p := Select(files, nil, false)
			p.Source = source
			p.Reasons = append(p.Reasons, "Go dependency discovery unavailable: complete checks must diagnose the original build failure")
			return p, nil
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var pkg Package
			if err := decoder.Decode(&pkg); err == io.EOF {
				break
			} else if err != nil {
				return Plan{}, fmt.Errorf("decode Go dependency graph: %w", err)
			}
			realRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				return Plan{}, err
			}
			realDir, err := filepath.EvalSymlinks(pkg.Dir)
			if err != nil {
				return Plan{}, err
			}
			relative, err := filepath.Rel(realRoot, realDir)
			if err != nil {
				return Plan{}, err
			}
			pkg.Dir = filepath.ToSlash(relative)
			packages = append(packages, pkg)
		}
	}
	p := Select(files, packages, o.Full)
	p.Source = source
	return p, nil
}

// Read rejects stale plans; it never treats a plan as execution evidence.
func Read(ctx context.Context, root, path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return Plan{}, err
	}
	if p.Version != 1 {
		return Plan{}, fmt.Errorf("unknown validation plan version %d", p.Version)
	}
	source, _, err := snapshot(ctx, root, Options{Base: p.Source.BaseRef, Head: p.Source.Head, Worktree: p.Source.Worktree})
	if err != nil {
		return Plan{}, err
	}
	if source != p.Source {
		return Plan{}, errors.New("source, baseline or validation toolchain changed; regenerate the impact plan")
	}
	return p, nil
}

// Write saves an ephemeral plan with no persistent execution or approval state.
func Write(path string, p Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

// RunQuality reuses the existing Makefile targets and selected package scope.
func RunQuality(ctx context.Context, root string, p Plan) error {
	if slices.Equal(p.QualityTargets, []string{"diff-check"}) {
		_, err := command(ctx, root, "git", "diff", "--check", p.Source.Base)
		return err
	}
	args := append([]string{}, p.QualityTargets...)
	args = append(args, "PKGS="+strings.Join(p.Packages, " "))
	cmd := exec.CommandContext(ctx, "make", args...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
