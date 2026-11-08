package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
)

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("package command output exceeds limit")
	}
	return b.Buffer.Write(p)
}

// command never invokes a shell and always contains descendants. Tool stderr
// is kept out of diagnostics because Git and npm can print authentication URLs.
func command(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	procx.Contain(c)
	var out limitedOutput
	c.Stdout = &out
	err := c.Run()
	procx.KillGroup(c)
	if err != nil {
		return nil, fmt.Errorf("%s package command failed: %w", name, err)
	}
	return out.Bytes(), nil
}

// Import acquires an explicit source once. No personal-home relationship or
// upstream watcher survives the import, and npm lifecycle scripts never run.
func Import(ctx context.Context, in protocol.ExtensionImport) (protocol.ExtensionPackage, error) {
	var out protocol.ExtensionPackage
	temp, err := os.MkdirTemp("", "tend-extension-import-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(temp)
	root := in.Path
	switch in.Source.Type {
	case "local":
		if root == "" {
			return out, fmt.Errorf("local import requires a package path")
		}
	case "git":
		if in.Path != "" || in.Source.URL == "" || strings.HasPrefix(in.Source.URL, "-") || in.Source.Ref == "" || strings.HasPrefix(in.Source.Ref, "-") {
			return out, fmt.Errorf("git import requires a URL and explicit ref")
		}
		expectedCommit := in.Source.Commit
		verifySource := in.Source
		if verifySource.Commit == "" {
			verifySource.Commit = strings.Repeat("0", 40)
		}
		if err = ValidateSource(verifySource); err != nil {
			return out, err
		}
		root = filepath.Join(temp, "git")
		if _, err = command(ctx, temp, "git", "-c", "core.hooksPath=", "clone", "--no-checkout", "--no-recurse-submodules", "--", in.Source.URL, root); err != nil {
			return out, err
		}
		raw, err := command(ctx, root, "git", "rev-parse", "--verify", "--end-of-options", in.Source.Ref+"^{commit}")
		if err != nil {
			return out, err
		}
		in.Source.Commit = strings.TrimSpace(string(raw))
		if !commitID.MatchString(in.Source.Commit) || expectedCommit != "" && expectedCommit != in.Source.Commit {
			return out, fmt.Errorf("git did not resolve a commit")
		}
		if _, err = command(ctx, root, "git", "-c", "core.hooksPath=", "-c", "submodule.recurse=false", "checkout", "--detach", in.Source.Commit); err != nil {
			return out, err
		}
	case "npm":
		if in.Path != "" || !regexp.MustCompile(`^(@[a-zA-Z0-9_-]+/)?[a-zA-Z0-9_.-]+$`).MatchString(in.Source.Package) || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?(?:\+[a-zA-Z0-9.-]+)?$`).MatchString(in.Source.Version) {
			return out, fmt.Errorf("npm import requires a package name and exact version")
		}
		raw, err := command(ctx, temp, "npm", "pack", "--ignore-scripts", "--json", "--", in.Source.Package+"@"+in.Source.Version)
		if err != nil {
			return out, err
		}
		var results []struct {
			Filename  string `json:"filename"`
			Integrity string `json:"integrity"`
			Version   string `json:"version"`
		}
		if json.Unmarshal(raw, &results) != nil || len(results) != 1 || filepath.Base(results[0].Filename) != results[0].Filename || results[0].Version != in.Source.Version {
			return out, fmt.Errorf("npm returned an unexpected package")
		}
		f, err := os.Open(filepath.Join(temp, results[0].Filename))
		if err != nil {
			return out, err
		}
		packed, readErr := io.ReadAll(io.LimitReader(f, MaxArchiveBytes+1))
		_ = f.Close()
		if readErr != nil || int64(len(packed)) > MaxArchiveBytes {
			return out, fmt.Errorf("npm package exceeds limit")
		}
		digest := sha512.Sum512(packed)
		integrity := "sha512-" + base64.StdEncoding.EncodeToString(digest[:])
		if integrity != results[0].Integrity || in.Source.Integrity != "" && in.Source.Integrity != integrity {
			return out, fmt.Errorf("npm integrity mismatch")
		}
		in.Source.Integrity = integrity
		gz, err := gzip.NewReader(bytes.NewReader(packed))
		if err != nil {
			return out, err
		}
		defer gz.Close()
		// Normalize npm's package/ prefix, preserving modes and rejecting links.
		var normalized bytes.Buffer
		tw := tar.NewWriter(&normalized)
		tr := tar.NewReader(gz)
		var size int64
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return out, err
			}
			if h.Typeflag == tar.TypeDir {
				continue
			}
			if !strings.HasPrefix(h.Name, "package/") || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > MaxArchiveBytes-size {
				return out, fmt.Errorf("invalid npm archive")
			}
			size += h.Size
			h.Name = strings.TrimPrefix(h.Name, "package/")
			if err = tw.WriteHeader(h); err != nil {
				return out, err
			}
			if _, err = io.CopyN(tw, tr, h.Size); err != nil {
				return out, err
			}
			if int64(normalized.Len()) > MaxArchiveBytes {
				return out, fmt.Errorf("npm archive exceeds limit")
			}
		}
		if err = tw.Close(); err != nil {
			return out, err
		}
		root = filepath.Join(temp, "npm")
		if err = ExtractPackage(ctx, normalized.Bytes(), root); err != nil {
			return out, err
		}
	default:
		return out, fmt.Errorf("unsupported import source")
	}
	if err = ValidateSource(in.Source); err != nil {
		return out, err
	}
	if in.Source.Subdir != "" {
		root, err = sourceSubdirectory(root, in.Source.Subdir)
		if err != nil {
			return out, err
		}
	}

	raw, err := Pack(ctx, root)
	if err != nil {
		return out, err
	}
	out.Resource, err = ResourceFromPackage(ctx, in, raw)
	if err != nil {
		return out, err
	}
	out.Content = raw
	return out, nil
}

// VerifyContent authenticates downloaded bytes before extraction or loading.
func VerifyContent(r protocol.ExtensionResource, raw []byte) error {
	if int64(len(raw)) != r.Size {
		return fmt.Errorf("package size mismatch")
	}
	want, err := hex.DecodeString(r.ContentSHA256)
	if err != nil || len(want) != 32 || Hash(raw) != r.ContentSHA256 {
		return fmt.Errorf("package digest mismatch")
	}
	return nil
}

func sourceSubdirectory(root, subdir string) (string, error) {
	parent, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	selected, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(subdir)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(parent, selected)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("selected subdirectory escapes package source")
	}
	return selected, nil
}
