package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runAttachment(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 && args[0] == "upload" {
		return runAttachmentUpload(ctx, e, args[1:])
	}
	if len(args) == 0 || args[0] != "download" {
		return usagef("attachment download <file_id> --out <path>")
	}
	fs := newFlags(e, "attachment download")
	out := fs.String("out", "", "save the original file at this path")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || *out == "" {
		return usagef("attachment download requires a file ID and --out path")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	path, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err := c.downloadAttachment(ctx, pos[0], path); err != nil {
		return err
	}
	_, err = fmt.Fprintln(e.Stdout, path)
	return err
}

func (c *client) downloadAttachment(ctx context.Context, id, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.server, "/")+"/api/v1/conversation/attachments/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	// File transfers follow caller cancellation, not the ordinary JSON RPC
	// deadline. Keep the configured transport and redirect behavior unchanged.
	client := *c.http
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var result protocol.APIResponse
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result) == nil && result.Error != nil {
			return result.Error
		}
		return fmt.Errorf("download attachment: HTTP %d", resp.StatusCode)
	}
	expected := resp.Header.Get("X-Content-SHA256")
	digestBytes, err := hex.DecodeString(expected)
	if err != nil || len(digestBytes) != sha256.Size || resp.ContentLength < 0 {
		return errors.New("download attachment: missing size or SHA256 manifest")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tend-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, digest), resp.Body)
	if err != nil {
		return fmt.Errorf("download attachment: %w", err)
	}
	if n != resp.ContentLength || hex.EncodeToString(digest.Sum(nil)) != strings.ToLower(expected) {
		return errors.New("download attachment failed integrity verification")
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
