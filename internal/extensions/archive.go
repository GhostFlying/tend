package extensions

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Archives are uncompressed deterministic tar files. Every consumer enforces
// the same limits before making the content available to a native process.
const MaxArchiveBytes int64 = 64 << 20
const maxPackageFiles = 10000

type PackageEntry struct {
	Name       string
	Executable bool
	Data       []byte
}

// Pack materializes links inside the selected package. A link outside the
// package, special file or concurrent source mutation fails the import.
func Pack(ctx context.Context, root string) ([]byte, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	read := func() ([]byte, error) {
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		count := 0
		var visit func(string, string, map[string]bool) error
		visit = func(source, name string, stack map[string]bool) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			real, err := filepath.EvalSymlinks(source)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, real)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("package link escapes selected root")
			}
			info, err := os.Stat(real)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if stack[real] {
					return fmt.Errorf("package link cycle")
				}
				stack[real] = true
				defer delete(stack, real)
				children, err := os.ReadDir(real)
				if err != nil {
					return err
				}
				for _, child := range children {
					if child.Name() == ".git" {
						continue
					}
					if err := visit(filepath.Join(real, child.Name()), path.Join(name, child.Name()), stack); err != nil {
						return err
					}
				}
				return nil
			}
			count++
			if !info.Mode().IsRegular() || count > maxPackageFiles || info.Size() > MaxArchiveBytes || int64(buf.Len())+info.Size()+2048 > MaxArchiveBytes {
				return fmt.Errorf("unsupported or oversized package")
			}
			mode := int64(0644)
			if info.Mode()&0111 != 0 {
				mode = 0755
			}
			if err := w.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: info.Size(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
				return err
			}
			f, err := os.Open(real)
			if err != nil {
				return err
			}
			n, copyErr := io.CopyN(w, f, info.Size())
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != info.Size() {
				return fmt.Errorf("package changed while importing")
			}
			return nil
		}
		if err := visit(root, "", map[string]bool{}); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		if int64(buf.Len()) > MaxArchiveBytes {
			return nil, fmt.Errorf("package is too large")
		}
		return buf.Bytes(), nil
	}
	first, err := read()
	if err != nil {
		return nil, err
	}
	second, err := read()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(first, second) {
		return nil, fmt.Errorf("package changed while importing")
	}
	return first, nil
}

// ReadPackage rejects links, duplicate names and alternate spellings before
// extraction. Package metadata is deliberately independent of host paths.
func ReadPackage(ctx context.Context, raw []byte) ([]PackageEntry, error) {
	if len(raw) == 0 || int64(len(raw)) > MaxArchiveBytes {
		return nil, fmt.Errorf("invalid package size")
	}
	r := tar.NewReader(bytes.NewReader(raw))
	entries := []PackageEntry{}
	seen := map[string]bool{}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || h.Name == "." || path.IsAbs(h.Name) || path.Clean(h.Name) != h.Name || strings.Contains(h.Name, `\`) || h.Name == ".." || strings.HasPrefix(h.Name, "../") || seen[h.Name] || len(entries) >= maxPackageFiles || h.Size < 0 || h.Size > MaxArchiveBytes-total {
			return nil, fmt.Errorf("invalid package entry")
		}
		for parent := path.Dir(h.Name); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return nil, fmt.Errorf("package file is also a directory")
			}
		}
		for name := range seen {
			if strings.HasPrefix(name, h.Name+"/") {
				return nil, fmt.Errorf("package directory is also a file")
			}
		}
		seen[h.Name] = true
		total += h.Size
		data, err := io.ReadAll(io.LimitReader(r, h.Size+1))
		if err != nil || int64(len(data)) != h.Size {
			return nil, fmt.Errorf("incomplete package entry")
		}
		entries = append(entries, PackageEntry{Name: h.Name, Executable: h.Mode&0111 != 0, Data: data})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("empty package")
	}
	return entries, nil
}

// ValidatePackage checks the native entry-point layout. Codex remains the
// authority for parsing skills and loading plugins during view validation.
func ValidatePackage(ctx context.Context, kind string, raw []byte) error {
	entries, err := ReadPackage(ctx, raw)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if kind == "skill" && e.Name == "SKILL.md" && len(e.Data) != 0 {
			return nil
		}
		if kind == "plugin" && e.Name == ".codex-plugin/plugin.json" {
			var manifest map[string]any
			if json.Unmarshal(e.Data, &manifest) != nil {
				return fmt.Errorf("invalid plugin manifest")
			}
			if name, ok := manifest["name"].(string); !ok || name == "" {
				return fmt.Errorf("plugin manifest requires a name")
			}
			return nil
		}
	}
	return fmt.Errorf("package is missing its native %s entry point", kind)
}

// ExtractPackage only writes into a newly created private directory. Failure
// removes that directory; callers publish it atomically after native validation.
func ExtractPackage(ctx context.Context, raw []byte, destination string) (err error) {
	entries, err := ReadPackage(ctx, raw)
	if err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		file := filepath.Join(destination, filepath.FromSlash(e.Name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if e.Executable {
			mode = 0700
		}
		if err := os.WriteFile(file, e.Data, mode); err != nil {
			return err
		}
	}
	return nil
}

func ResourceFromPackage(ctx context.Context, in protocol.ExtensionImport, raw []byte) (protocol.ExtensionResource, error) {
	r := protocol.ExtensionResource{Kind: in.Kind, ID: in.ID, Version: in.Version, Source: in.Source, ContentSHA256: Hash(raw), Size: int64(len(raw))}
	if err := ValidateResource(r); err != nil {
		return r, err
	}
	return r, ValidatePackage(ctx, r.Kind, raw)
}
