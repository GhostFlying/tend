package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/GhostFlying/tend/internal/release"
)

func runComponent(ctx context.Context, arch, commit, output string) error {
	if (arch != "amd64" && arch != "arm64") || output == "" || len(commit) != 40 {
		return errors.New("package: arch, commit and output are required")
	}
	head, err := git(ctx, "rev-parse", "HEAD")
	if err != nil || head != commit {
		return errors.New("package: source differs from requested commit")
	}
	status, err := git(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil || status != "" {
		return errors.New("package: tracked source must be clean")
	}
	stamp, err := git(ctx, "show", "-s", "--format=%ct", commit)
	if err != nil {
		return err
	}
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(output, ".maintainer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "component")
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "tend-maintainer")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=true", "-o", path, "./cmd/tend-maintainer")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("package: build maintainer: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	c := release.Component{Format: 1, ReleaseID: "main-" + commit, Commit: commit, State: release.Range{Min: 1, Max: 1}, Artifact: release.Artifact{Role: "maintainer", OS: "linux", Arch: arch, Path: "tend-maintainer", Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}}
	if err := c.Validate(); err != nil {
		return err
	}
	raw, err = json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "component.json"), raw, 0600); err != nil {
		return err
	}
	if _, err := release.LoadComponent(dir); err != nil {
		return err
	}
	name := "tend-maintainer-main-" + commit + "-linux-" + arch
	archive := filepath.Join(tmp, name+".tar.gz")
	if err := pack(dir, archive, time.Unix(seconds, 0).UTC()); err != nil {
		return err
	}
	if err := publishFile(archive, filepath.Join(output, name+".tar.gz")); err != nil {
		return err
	}
	return publishFile(filepath.Join(dir, "component.json"), filepath.Join(output, name+".json"))
}
