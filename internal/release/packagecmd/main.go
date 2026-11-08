// packagecmd builds reproducible, source-bound Linux distribution archives.
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	arch := flag.String("arch", "", "Linux target architecture")
	commit := flag.String("commit", "", "exact source commit")
	out := flag.String("output", "", "output directory outside tracked source")
	amd64 := flag.String("input-amd64", "", "verified amd64 bundle to assemble")
	arm64 := flag.String("input-arm64", "", "verified arm64 bundle to assemble")
	flag.Parse()
	var err error
	if *amd64 != "" || *arm64 != "" {
		err = merge(ctx, *amd64, *arm64, *commit, *out)
	} else {
		err = run(ctx, *arch, *commit, *out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func merge(ctx context.Context, amd64, arm64, commit, output string) error {
	if amd64 == "" || arm64 == "" || output == "" {
		return errors.New("package: both platform inputs and output are required")
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(output, ".assemble-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bundle, err := release.MergeTo(amd64, arm64, filepath.Join(tmp, "fleet"))
	if err != nil {
		return err
	}
	if bundle.Set.Commit != commit {
		return errors.New("package: fleet source differs from checked commit")
	}
	for _, a := range bundle.Artifacts() {
		source, err := release.ExecutableSource(filepath.Join(bundle.Dir, a.Path))
		if err != nil || source.Modified || source.Commit != commit {
			return errors.New("package: fleet executable source differs")
		}
	}
	stamp, err := git(ctx, "show", "-s", "--format=%ct", commit)
	if err != nil {
		return err
	}
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return err
	}
	name := "tend-main-" + commit + "-linux-fleet"
	archive := filepath.Join(tmp, name+".tar.gz")
	if err := pack(bundle.Dir, archive, time.Unix(seconds, 0).UTC()); err != nil {
		return err
	}
	if err := publishFile(archive, filepath.Join(output, name+".tar.gz")); err != nil {
		return err
	}
	return publishFile(filepath.Join(bundle.Dir, "release-set.json"), filepath.Join(output, name+".json"))
}

func git(ctx context.Context, args ...string) (string, error) {
	raw, err := exec.CommandContext(ctx, "git", args...).Output()
	return strings.TrimSpace(string(raw)), err
}

func run(ctx context.Context, arch, commit, output string) error {
	if (arch != "amd64" && arch != "arm64") || output == "" || len(commit) != 40 {
		return errors.New("package: --arch, --commit and --output are required")
	}
	head, err := git(ctx, "rev-parse", "HEAD")
	if err != nil || head != commit {
		return errors.New("package: source does not match requested commit")
	}
	status, err := git(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil || status != "" {
		return errors.New("package: tracked source must be clean")
	}
	if _, err := os.Stat("internal/webui/dist/app/index.html"); err != nil {
		return fmt.Errorf("package: build the real web app first: %w", err)
	}
	stamp, err := git(ctx, "show", "-s", "--format=%ct", commit)
	if err != nil {
		return err
	}
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return err
	}
	root, err := os.MkdirTemp(output, ".package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	name := "tend-main-" + commit + "-linux-" + arch
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	serverMigrations, err := filepath.Glob("internal/store/migrations/*.sql")
	if err != nil || len(serverMigrations) == 0 {
		return errors.New("package: server migrations unavailable")
	}
	runnerMigrations, err := filepath.Glob("internal/runner/journal/migrations/*.sql")
	if err != nil || len(runnerMigrations) == 0 {
		return errors.New("package: runner migrations unavailable")
	}
	m := release.Manifest{Format: 1, ReleaseID: "main-" + commit, Protocol: release.Range{Min: protocol.PreviousRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: 1, Max: int64(len(serverMigrations))}, RunnerJournal: release.Range{Min: 1, Max: int64(len(runnerMigrations))}}}
	set := release.Set{Format: 1, ReleaseID: m.ReleaseID, Commit: commit, OS: "linux", Arch: arch}
	for _, role := range []string{"server", "runner", "updater", "cli"} {
		binary := "tend-" + role
		if role == "cli" {
			binary = "tend"
		}
		path := filepath.Join(dir, binary)
		cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=true", "-o", path, "./cmd/"+binary)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("package: build %s: %w", role, err)
		}
		source, err := release.ExecutableSource(path)
		if err != nil || source.Modified || source.Commit != commit {
			return fmt.Errorf("package: %s has a different or modified source", role)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		a := release.Artifact{Role: role, OS: "linux", Arch: arch, Path: binary, Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
		set.Artifacts = append(set.Artifacts, a)
		if role == "server" || role == "runner" {
			m.Artifacts = append(m.Artifacts, a)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	set.ManifestSHA256 = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(set)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "release-set.json"), raw, 0o600); err != nil {
		return err
	}
	if _, err := release.Load(dir); err != nil {
		return err
	}
	archive := filepath.Join(root, name+".tar.gz")
	if err := pack(dir, archive, time.Unix(seconds, 0).UTC()); err != nil {
		return err
	}
	for _, file := range []struct{ src, dst string }{{archive, name + ".tar.gz"}, {filepath.Join(dir, "release-set.json"), name + ".json"}} {
		if err := publishFile(file.src, filepath.Join(output, file.dst)); err != nil {
			return err
		}
	}
	return nil
}

func publishFile(source, target string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(target); err == nil {
		if sha256.Sum256(old) != sha256.Sum256(raw) {
			return errors.New("package: existing output has different bytes")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, target)
}

func pack(dir, target string, stamp time.Time) (result error) {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	defer func() { result = errors.Join(result, tw.Close(), gz.Close(), f.Close()) }()
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("package: non-regular archive input")
		}
		mode := int64(0o755)
		if strings.HasSuffix(entry.Name(), ".json") {
			mode = 0o644
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(relative), Mode: mode, Size: info.Size(), ModTime: stamp}); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		if err := errors.Join(copyErr, in.Close()); err != nil {
			return err
		}
		return nil
	})
}
