package release

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
	"sync"
	"testing"
	"time"
)

type officialTransport func(*http.Request) (*http.Response, error)

func (f officialTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func officialFixture(t *testing.T) (officialRelease, []byte) {
	t.Helper()
	tag := "main-" + strings.Repeat("a", 40)
	m := Manifest{Format: 1, ReleaseID: tag, Protocol: Range{1, 2}, Storage: Storage{Range{1, 2}, Range{1, 2}}}
	s := Set{Format: 2, Commit: tag[5:], ReleaseID: tag, OS: "linux", Arch: "multi"}
	files := map[string][]byte{}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, role := range []string{"server", "runner", "updater", "cli"} {
			path := "linux-" + arch + "/tend-" + role
			body := []byte(path)
			h := sha256.Sum256(body)
			a := Artifact{Role: role, OS: "linux", Arch: arch, Path: path, Size: int64(len(body)), SHA256: hex.EncodeToString(h[:])}
			files[path] = body
			s.Artifacts = append(s.Artifacts, a)
			if role == "server" || role == "runner" {
				m.Artifacts = append(m.Artifacts, a)
			}
		}
	}
	files["manifest.json"], _ = json.Marshal(m)
	h := sha256.Sum256(files["manifest.json"])
	s.ManifestSHA256 = hex.EncodeToString(h[:])
	files["release-set.json"], _ = json.Marshal(s)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(body)), Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	h = sha256.Sum256(buf.Bytes())
	name := "tend-" + tag + "-linux-fleet.tar.gz"
	r := officialRelease{Tag: tag, Published: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Assets: []officialAsset{{Name: name, Size: int64(buf.Len()), Digest: "sha256:" + hex.EncodeToString(h[:]), URL: "https://github.com/GhostFlying/tend/releases/download/" + tag + "/" + name}}}
	return r, buf.Bytes()
}

func TestOfficialDownloadPinsOriginalSelectionAcrossFailureAndRestart(t *testing.T) {
	r, body := officialFixture(t)
	var apiCalls, downloads int
	client := &http.Client{Transport: officialTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" {
			t.Fatal("service credentials leaked")
		}
		var raw []byte
		if strings.HasPrefix(req.URL.String(), officialAPI) {
			apiCalls++
			if apiCalls > 1 {
				t.Fatal("original request reselected a release")
			}
			raw, _ = json.Marshal([]officialRelease{r})
		} else {
			downloads++
			if downloads == 1 {
				return nil, errors.New("network unavailable")
			}
			raw = body
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})}
	dir := filepath.Join(t.TempDir(), "request")
	if _, err := FetchOfficial(t.Context(), client, dir, "", "original-input"); err == nil {
		t.Fatal("failed download accepted")
	}
	b, err := FetchOfficial(t.Context(), client, dir, "", "original-input")
	if err != nil {
		t.Fatal(err)
	}
	if b.Set.Format != 2 || b.Set.ReleaseID != r.Tag || len(b.Artifacts()) != 8 {
		t.Fatal(b.Set)
	}
	if _, err := FetchOfficial(t.Context(), client, dir, "", "changed-input"); err == nil {
		t.Fatal("changed original input accepted")
	}
	if _, err := FetchOfficial(t.Context(), client, dir, "", "original-input"); err != nil {
		t.Fatal(err)
	}
	if apiCalls != 1 || downloads != 2 {
		t.Fatal(apiCalls, downloads)
	}
}

func TestOfficialCommitAndConcurrentRequestsKeepOneCompleteIdentity(t *testing.T) {
	r, body := officialFixture(t)
	client := &http.Client{Transport: officialTransport(func(req *http.Request) (*http.Response, error) {
		raw := body
		if strings.HasPrefix(req.URL.String(), officialAPI) {
			if req.URL.String() != officialAPI+"/tags/"+r.Tag {
				return nil, errors.New("commit was not pinned")
			}
			raw, _ = json.Marshal(r)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})}
	dir := filepath.Join(t.TempDir(), "original-request")
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			b, err := FetchOfficial(context.Background(), client, dir, r.Tag, "original")
			if err != nil {
				t.Error(err)
				return
			}
			if b.Set.Commit != r.Tag[5:] {
				t.Error("commit changed")
			}
		})
	}
	wg.Wait()
}

func TestOfficialSelectionRejectsDraftMissingOrForeignAssetsAndCorruption(t *testing.T) {
	for _, kind := range []string{"draft", "service-only", "foreign", "corrupt", "wrong-commit"} {
		t.Run(kind, func(t *testing.T) {
			r, body := officialFixture(t)
			switch kind {
			case "draft":
				r.Draft = true
			case "service-only":
				r.Assets = nil
			case "foreign":
				r.Assets[0].URL = "https://example.invalid/archive"
			case "corrupt":
				body = []byte("corrupt")
			case "wrong-commit":
				r.Tag = "main-" + strings.Repeat("b", 40)
			}
			client := &http.Client{Transport: officialTransport(func(req *http.Request) (*http.Response, error) {
				raw := body
				if strings.HasPrefix(req.URL.String(), officialAPI) {
					raw, _ = json.Marshal([]officialRelease{r})
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})}
			dir := filepath.Join(t.TempDir(), "request")
			if _, err := FetchOfficial(t.Context(), client, dir, "", "original"); err == nil {
				t.Fatal("invalid release accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "bundle")); !os.IsNotExist(err) {
				t.Fatal("invalid bundle published", err)
			}
		})
	}
}

func TestOfficialArchiveRejectsTraversalLinksAndDuplicateEntries(t *testing.T) {
	for _, kind := range []string{"../escape", "/absolute", "symlink", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tw := tar.NewWriter(gz)
			h := &tar.Header{Name: kind, Size: 1, Mode: 0o600}
			if kind == "symlink" {
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "outside"
				h.Size = 0
			}
			n := 1
			if kind == "duplicate" {
				n = 2
			}
			for range n {
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if h.Size > 0 {
					_, _ = tw.Write([]byte("x"))
				}
			}
			_ = tw.Close()
			_ = gz.Close()
			if err := extractOfficial(t.Context(), &b, t.TempDir()); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
