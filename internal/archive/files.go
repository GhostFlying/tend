package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func regularFile(path string) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("archive: expected regular file: %s", path)
	}
	return info, nil
}
func payloadPath(root, relative string) (string, error) {
	if !fs.ValidPath(relative) || strings.Contains(relative, `\`) {
		return "", errors.New("archive: invalid relative payload path")
	}
	return filepath.Join(root, filepath.FromSlash(relative)), nil
}
func openPayload(root, relative string) (*os.File, error) {
	path, err := payloadPath(root, relative)
	if err != nil {
		return nil, err
	}
	if _, err = regularFile(path); err != nil {
		return nil, err
	}
	return os.Open(path)
}
func containsPath(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func copyFile(ctx context.Context, source, destination string) error {
	if _, err := regularFile(source); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := io.Copy(output, contextReader{ctx, input})
	if writeErr == nil {
		writeErr = output.Sync()
	}
	return errors.Join(writeErr, output.Close())
}
func writeBytes(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}
func hashFile(ctx context.Context, path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, contextReader{ctx, f})
	return size, hex.EncodeToString(digest.Sum(nil)), err
}
func payload(ctx context.Context, root, path, kind string) (Payload, error) {
	absolute, err := payloadPath(root, path)
	if err != nil {
		return Payload{}, err
	}
	size, hash, err := hashFile(ctx, absolute)
	return Payload{Path: path, Kind: kind, Size: size, SHA256: hash}, err
}
func readJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errors.New("archive: expected one JSON value")
	}
	return nil
}

// ReadJSON decodes a local explicit selection/resource file, rejecting unknown fields.
func ReadJSON(path string, value any) error { return readJSON(path, value) }

// Sync copied directory entries before publishing the completed manifest.
func syncPayloadDirectories(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err = syncDirectory(directories[i]); err != nil {
			return err
		}
	}
	return nil
}
