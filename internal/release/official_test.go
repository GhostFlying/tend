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

func officialFixtureResponse(r officialRelease, archive []byte, req *http.Request) *http.Response {
	raw := archive
	if req.URL.String() == officialFeed {
		raw = []byte(`<feed xmlns="http://www.w3.org/2005/Atom">`)
		if !r.Draft {
			raw = append(raw, []byte(`<entry><updated>`+r.Published.Format(time.RFC3339)+`</updated><link href="https://github.com/GhostFlying/tend/releases/tag/`+r.Tag+`"/></entry>`)...)
		}
		raw = append(raw, []byte(`</feed>`)...)
	} else if strings.HasSuffix(req.URL.Path, "/SHA256SUMS") {
		raw = nil
		for _, a := range r.Assets {
			raw = append(raw, []byte(strings.TrimPrefix(a.Digest, "sha256:")+"  "+a.Name+"\n")...)
		}
	}
	size := int64(len(raw))
	if req.Method == http.MethodHead && len(r.Assets) > 0 {
		size = r.Assets[0].Size
		raw = nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: size, Body: io.NopCloser(bytes.NewReader(raw))}
}

func TestOfficialDownloadPinsOriginalSelectionAcrossFailureAndRestart(t *testing.T) {
	r, body := officialFixture(t)
	var discoveries, downloads int
	client := &http.Client{Transport: officialTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" {
			t.Fatal("service credentials leaked")
		}
		if req.URL.String() == officialFeed {
			discoveries++
			if discoveries > 1 {
				t.Fatal("original request reselected a release")
			}
		}
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, ".tar.gz") {
			downloads++
			if downloads == 1 {
				return nil, errors.New("network unavailable")
			}
		}
		return officialFixtureResponse(r, body, req), nil
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
	if discoveries != 1 || downloads != 2 {
		t.Fatal(discoveries, downloads)
	}
}

func TestOfficialCommitAndConcurrentRequestsKeepOneCompleteIdentity(t *testing.T) {
	r, body := officialFixture(t)
	client := &http.Client{Transport: officialTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == officialFeed || !strings.Contains(req.URL.Path, "/"+r.Tag+"/") {
			return nil, errors.New("commit was not pinned")
		}
		return officialFixtureResponse(r, body, req), nil
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

func TestOfficialMetadataFailureRetainsDiscoveredTag(t *testing.T) {
	for _, phase := range []string{"checksums", "head"} {
		t.Run(phase, func(t *testing.T) {
			r, body := officialFixture(t)
			discoveries, failures := 0, 0
			client := &http.Client{Transport: officialTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() == officialFeed {
					discoveries++
					if discoveries > 1 {
						return nil, errors.New("must not rediscover after metadata failure")
					}
				}
				if failures == 0 && (phase == "checksums" && strings.HasSuffix(req.URL.Path, "/SHA256SUMS") || phase == "head" && req.Method == http.MethodHead) {
					failures++
					return nil, errors.New("metadata unavailable")
				}
				return officialFixtureResponse(r, body, req), nil
			})}
			dir := filepath.Join(t.TempDir(), "original")
			if _, err := FetchOfficial(t.Context(), client, dir, "", "original"); err == nil {
				t.Fatal("failure accepted")
			}
			b, err := FetchOfficial(t.Context(), client, dir, "", "original")
			if err != nil || b.Set.Commit != r.Tag[5:] || discoveries != 1 {
				t.Fatal(b, err, discoveries)
			}
		})
	}
}

func TestOfficialFeedSelectsNewestPublishedMainRatherThanFirstEntry(t *testing.T) {
	r, body := officialFixture(t)
	older := "main-" + strings.Repeat("b", 40)
	feed := `<feed><entry><updated>2026-01-01T00:00:00Z</updated><link href="https://github.com/GhostFlying/tend/releases/tag/` + older + `"/></entry><entry><updated>2026-01-02T00:00:00Z</updated><link href="https://github.com/GhostFlying/tend/releases/tag/` + r.Tag + `"/></entry><entry><updated>2026-01-03T00:00:00Z</updated><link href="https://github.com/GhostFlying/tend/releases/tag/unrelated-component"/></entry></feed>`
	client := &http.Client{Transport: officialTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == officialFeed {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(feed))}, nil
		}
		if !strings.Contains(req.URL.Path, "/"+r.Tag+"/") {
			return nil, errors.New("older or unrelated target selected")
		}
		return officialFixtureResponse(r, body, req), nil
	})}
	b, err := FetchOfficial(t.Context(), client, filepath.Join(t.TempDir(), "request"), "", "original")
	if err != nil || b.Set.ReleaseID != r.Tag {
		t.Fatal(b, err)
	}
}

func TestOfficialSelectionRejectsDraftMissingOrConflictingAssetsAndCorruption(t *testing.T) {
	for _, kind := range []string{"draft", "service-only", "duplicate-checksum", "corrupt", "wrong-commit"} {
		t.Run(kind, func(t *testing.T) {
			r, body := officialFixture(t)
			switch kind {
			case "draft":
				r.Draft = true
			case "service-only":
				r.Assets = nil
			case "duplicate-checksum":
				r.Assets = append(r.Assets, r.Assets[0])
			case "corrupt":
				body = []byte("corrupt")
			case "wrong-commit":
				r.Tag = "main-" + strings.Repeat("b", 40)
			}
			client := &http.Client{Transport: officialTransport(func(req *http.Request) (*http.Response, error) { return officialFixtureResponse(r, body, req), nil })}
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
