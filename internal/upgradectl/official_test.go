package upgradectl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/updater"
)

type officialTestTransport func(*http.Request) (*http.Response, error)

func (f officialTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDefaultOfficialUpgradeAndOriginalRequestReplay(t *testing.T) {
	c, backend := testController(t)
	dir := testBundle(t)
	b, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := b.Manifest
	s := *b.Set
	m.ReleaseID = "main-" + s.Commit
	s.ReleaseID, s.Arch = m.ReleaseID, "multi"
	other := "arm64"
	if s.Artifacts[0].Arch == other {
		other = "amd64"
	}
	for _, a := range b.Set.Artifacts {
		body, err := os.ReadFile(filepath.Join(dir, a.Path))
		if err != nil {
			t.Fatal(err)
		}
		a.Arch = other
		a.Path = other + "/" + a.Path
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, a.Path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, a.Path), body, 0o600); err != nil {
			t.Fatal(err)
		}
		s.Artifacts = append(s.Artifacts, a)
		if a.Role == "server" || a.Role == "runner" {
			m.Artifacts = append(m.Artifacts, a)
		}
	}
	raw, _ := json.Marshal(m)
	hash := sha256.Sum256(raw)
	s.ManifestSHA256 = hex.EncodeToString(hash[:])
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, "release-set.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(relative), Size: int64(len(body)), Mode: 0o755}); err != nil {
			return err
		}
		_, err = tw.Write(body)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	hash = sha256.Sum256(archive.Bytes())
	tag := s.ReleaseID
	name := "tend-" + tag + "-linux-fleet.tar.gz"
	apiCalls, downloads := 0, 0
	c.cfg.ReleaseHTTP = &http.Client{Transport: officialTestTransport(func(req *http.Request) (*http.Response, error) {
		body := archive.Bytes()
		if req.URL.Host == "api.github.com" {
			apiCalls++
			body, _ = json.Marshal([]any{map[string]any{"tag_name": tag, "draft": false, "published_at": time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "assets": []any{map[string]any{"name": name, "size": archive.Len(), "digest": "sha256:" + hex.EncodeToString(hash[:]), "browser_download_url": "https://github.com/GhostFlying/tend/releases/download/" + tag + "/" + name}}}})
		} else {
			downloads++
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	r := protocol.UpgradeStartRequest{RequestID: c.ids.New(protocol.PrefixRequest), UpgradeID: "official-default"}
	if _, err := c.Start(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if len(backend.commands) != 1 || apiCalls != 1 || downloads != 1 {
		t.Fatal(len(backend.commands), apiCalls, downloads)
	}
	staged, err := release.Load(filepath.Join(c.cfg.ReleaseDir, r.UpgradeID))
	if err != nil || staged.Set == nil || staged.Set.Format != 2 || staged.Set.Commit != s.Commit {
		t.Fatal(staged, err)
	}
	result, _ := json.Marshal(protocol.UpgradeView{UpgradeID: r.UpgradeID, Revision: 2, Status: "running", Phase: "server_updater"})
	backend.preflightResult = &protocol.Operation{Status: protocol.OpSucceeded, Result: result}
	c.cfg.ObserveUpdater = func(_ context.Context, _ string) (updater.SelfUpdateStatus, error) {
		return updater.SelfUpdateStatus{}, errors.New("socket temporarily unavailable")
	}
	view, err := c.Start(t.Context(), r)
	if err != nil || view.UpgradeID != r.UpgradeID || len(backend.commands) != 1 || apiCalls != 1 || downloads != 1 {
		t.Fatal(view, err, len(backend.commands), apiCalls, downloads)
	}
	r.ParkIdleManagers = true
	if _, err := c.Start(t.Context(), r); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatal("changed input reused original selection", err)
	}
}
