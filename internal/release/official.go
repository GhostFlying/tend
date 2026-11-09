package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const officialFeed = "https://github.com/GhostFlying/tend/releases.atom"

type officialAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type officialRelease struct {
	Tag       string          `json:"tag_name"`
	Draft     bool            `json:"draft"`
	Published time.Time       `json:"published_at"`
	Assets    []officialAsset `json:"assets"`
}
type officialSelection struct {
	Input   string          `json:"input"`
	Release officialRelease `json:"release"`
	Archive officialAsset   `json:"archive"`
}

// OfficialTag validates an immutable main source selector. An empty selector
// chooses the newest published main release, including prereleases.
func OfficialTag(s string) bool {
	if !strings.HasPrefix(s, "main-") || len(s) != 45 {
		return false
	}
	b, err := hex.DecodeString(s[5:])
	return err == nil && len(b) == 20 && s[5:] == hex.EncodeToString(b)
}

// FetchOfficial pins selection before downloading any archive. The private
// request directory belongs to one immutable caller input; a restart never
// resolves a newer release for that request. No business operation is sent here.
func FetchOfficial(ctx context.Context, client *http.Client, directory, selector, input string) (*Bundle, error) {
	if selector != "" && !OfficialTag(selector) {
		return nil, errors.New("release: expected a complete main commit selector")
	}
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Minute}
	}
	if err := PinUpgradeInput(directory, input); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("release: official selection directory must be private")
	}
	pin := filepath.Join(directory, "selection.json")
	var selection officialSelection
	if raw, err := os.ReadFile(pin); err == nil {
		if err := json.Unmarshal(raw, &selection); err != nil {
			return nil, fmt.Errorf("release: original selection is unreadable: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		chosen, err := selectOfficial(ctx, client, directory, selector)
		if err != nil {
			return nil, err
		}
		selection = officialSelection{Input: input, Release: chosen}
		name := "tend-" + chosen.Tag + "-linux-fleet.tar.gz"
		for _, asset := range chosen.Assets {
			if asset.Name == name {
				if selection.Archive.Name != "" {
					return nil, errors.New("release: duplicate official fleet asset")
				}
				selection.Archive = asset
			}
		}
		if err := validateOfficial(selection, selector, input); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(selection)
		if err != nil {
			return nil, err
		}
		tmp, err := os.CreateTemp(directory, ".selection-")
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp.Name())
		_, writeErr := tmp.Write(raw)
		err = errors.Join(writeErr, tmp.Sync(), tmp.Close())
		if err != nil {
			return nil, err
		}
		// Link publishes only a completely written pin and cannot overwrite a
		// concurrent selector. The winning original request remains authoritative.
		if err := os.Link(tmp.Name(), pin); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if err := syncOfficialDir(directory); err != nil {
			return nil, err
		}
		raw, err = os.ReadFile(pin)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &selection); err != nil {
			return nil, err
		}
	}
	if err := validateOfficial(selection, selector, input); err != nil {
		return nil, err
	}
	// Persist newly created request-directory links as well as the pin. This
	// also reconciles a previous directory-sync failure before any download.
	for _, path := range []string{directory, filepath.Dir(directory), filepath.Dir(filepath.Dir(directory))} {
		if err := syncOfficialDir(path); err != nil {
			return nil, err
		}
	}
	destination := filepath.Join(directory, "bundle")
	if _, err := os.Lstat(destination); err == nil {
		return loadOfficial(destination, selection.Release.Tag)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	archive, err := os.CreateTemp(directory, ".download-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	response, err := officialGET(ctx, client, selection.Archive.URL)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(response.Body, selection.Archive.Size+1))
	closeErr := response.Body.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return nil, err
	}
	if n != selection.Archive.Size || hex.EncodeToString(hash.Sum(nil)) != strings.TrimPrefix(selection.Archive.Digest, "sha256:") {
		return nil, errors.New("release: official archive size or SHA-256 differs")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(directory, ".extract-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := extractOfficial(ctx, archive, tmp); err != nil {
		return nil, err
	}
	bundle, err := loadOfficial(tmp, selection.Release.Tag)
	if err != nil {
		return nil, err
	}
	staged, err := bundle.CopyTo(destination)
	if err != nil {
		// A concurrent fetch of this same pinned request may have published first.
		if existing, readErr := loadOfficial(destination, selection.Release.Tag); readErr == nil && existing.SetSHA256 == bundle.SetSHA256 {
			return existing, nil
		}
	}
	return staged, err
}

func validateOfficial(s officialSelection, selector, input string) error {
	if s.Input != input || !OfficialTag(s.Release.Tag) || s.Release.Draft || selector == "" && s.Release.Published.IsZero() || selector != "" && s.Release.Tag != selector {
		return errors.New("release: original official selection conflicts with this request")
	}
	a := s.Archive
	want := "tend-" + s.Release.Tag + "-linux-fleet.tar.gz"
	if a.Name != want || a.URL != "https://github.com/GhostFlying/tend/releases/download/"+s.Release.Tag+"/"+want || !strings.HasPrefix(a.Digest, "sha256:") || !ValidSHA(strings.TrimPrefix(a.Digest, "sha256:")) || a.Size <= 0 || a.Size > MaxArtifactBytes {
		return errors.New("release: official complete fleet asset or digest is missing")
	}
	return nil
}

// PinUpgradeInput binds every new local or official upgrade to its exact
// original input, including before a release has finished downloading.
func PinUpgradeInput(directory, input string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("release: upgrade request directory must be private")
	}
	raw, err := json.Marshal(struct {
		Input string `json:"input"`
	}{input})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".input-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(raw)
	if err := errors.Join(writeErr, temp.Sync(), temp.Close()); err != nil {
		return err
	}
	path := filepath.Join(directory, "input.json")
	if err := os.Link(temp.Name(), path); err != nil && !os.IsExist(err) {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(existing) != string(raw) {
		return errors.New("release: original upgrade input conflicts with this request")
	}
	for _, path := range []string{directory, filepath.Dir(directory), filepath.Dir(filepath.Dir(directory))} {
		if err := syncOfficialDir(path); err != nil {
			return err
		}
	}
	return nil
}

func selectOfficial(ctx context.Context, client *http.Client, directory, selector string) (officialRelease, error) {
	chosen := officialRelease{Tag: selector}
	tagPath := filepath.Join(directory, "tag.json")
	tagRaw, tagErr := os.ReadFile(tagPath)
	if tagErr == nil {
		if err := json.Unmarshal(tagRaw, &chosen); err != nil {
			return chosen, err
		}
	} else if !os.IsNotExist(tagErr) {
		return chosen, tagErr
	}
	if os.IsNotExist(tagErr) && selector == "" {
		response, err := officialGET(ctx, client, officialFeed)
		if err != nil {
			return chosen, err
		}
		var feed struct {
			Entries []struct {
				Updated time.Time `xml:"updated"`
				Links   []struct {
					Href string `xml:"href,attr"`
				} `xml:"link"`
			} `xml:"entry"`
		}
		err = xml.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&feed)
		closeErr := response.Body.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return chosen, err
		}
		// Feed order is not publication order. Compare every eligible entry before
		// pinning a target; editing or assembling the feed can reorder entries.
		for _, entry := range feed.Entries {
			for _, link := range entry.Links {
				tag, ok := strings.CutPrefix(link.Href, "https://github.com/GhostFlying/tend/releases/tag/")
				if ok && OfficialTag(tag) && entry.Updated.After(chosen.Published) {
					chosen.Tag, chosen.Published = tag, entry.Updated
					break
				}
			}
		}
		if chosen.Tag == "" {
			return chosen, errors.New("release: no published main release; provide --commit or --release")
		}
	}
	if os.IsNotExist(tagErr) {
		raw, err := json.Marshal(chosen)
		if err != nil {
			return chosen, err
		}
		tmp, err := os.CreateTemp(directory, ".tag-")
		if err != nil {
			return chosen, err
		}
		defer os.Remove(tmp.Name())
		_, writeErr := tmp.Write(raw)
		if err := errors.Join(writeErr, tmp.Sync(), tmp.Close()); err != nil {
			return chosen, err
		}
		if err := os.Link(tmp.Name(), tagPath); err != nil && !os.IsExist(err) {
			return chosen, err
		}
		raw, err = os.ReadFile(tagPath)
		if err != nil {
			return chosen, err
		}
		if err := json.Unmarshal(raw, &chosen); err != nil {
			return chosen, err
		}
	}
	if !OfficialTag(chosen.Tag) || selector != "" && chosen.Tag != selector || selector == "" && chosen.Published.IsZero() {
		return chosen, errors.New("release: original official tag conflicts with this request")
	}
	if err := syncOfficialDir(directory); err != nil {
		return chosen, err
	}
	base := "https://github.com/GhostFlying/tend/releases/download/" + chosen.Tag + "/"
	response, err := officialGET(ctx, client, base+"SHA256SUMS")
	if err != nil {
		return chosen, err
	}
	sums, readErr := io.ReadAll(io.LimitReader(response.Body, MaxManifestBytes+1))
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return chosen, err
	}
	if len(sums) > MaxManifestBytes {
		return chosen, errors.New("release: official checksums are oversized")
	}
	name := "tend-" + chosen.Tag + "-linux-fleet.tar.gz"
	asset := officialAsset{Name: name, URL: base + name}
	for _, line := range strings.Split(string(sums), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || parts[1] != name {
			continue
		}
		if asset.Digest != "" || !ValidSHA(parts[0]) {
			return chosen, errors.New("release: conflicting official fleet checksum")
		}
		asset.Digest = "sha256:" + parts[0]
	}
	if asset.Digest == "" {
		return chosen, errors.New("release: official complete fleet checksum is missing")
	}
	// Public checksum assets of a draft are not downloadable. An exact --commit
	// can therefore select an older published tag without traversing old releases.
	head, err := officialRequest(ctx, client, http.MethodHead, asset.URL)
	if err != nil {
		return chosen, err
	}
	asset.Size = head.ContentLength
	if err := head.Body.Close(); err != nil {
		return chosen, err
	}
	if asset.Size <= 0 || asset.Size > MaxArtifactBytes {
		return chosen, errors.New("release: official fleet size is unavailable or exceeds the limit")
	}
	chosen.Assets = []officialAsset{asset}
	return chosen, nil
}

func officialGET(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	return officialRequest(ctx, client, http.MethodGet, url)
}

func officialRequest(ctx context.Context, client *http.Client, method, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "tend-release")
	r, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if r.StatusCode != http.StatusOK {
		_ = r.Body.Close()
		return nil, fmt.Errorf("release: official download returned HTTP %d", r.StatusCode)
	}
	return r, nil
}

func loadOfficial(directory, tag string) (*Bundle, error) {
	b, err := Load(directory)
	if err != nil {
		return nil, err
	}
	if b.Set == nil || b.Set.Format != 2 || b.Set.Commit != strings.TrimPrefix(tag, "main-") || b.Set.ReleaseID != tag || b.Set.Arch != "multi" {
		return nil, errors.New("release: official archive is not the pinned format 2 fleet release")
	}
	return b, nil
}

func extractOfficial(ctx context.Context, input io.Reader, destination string) error {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	seen := map[string]bool{}
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if count >= 32 || !safeRelative(h.Name) || seen[h.Name] || h.Size < 0 || h.Size > MaxArtifactBytes {
			return errors.New("release: unsafe official archive entry")
		}
		seen[h.Name] = true
		total += h.Size
		if total > 8*MaxArtifactBytes {
			return errors.New("release: official archive exceeds the complete set limit")
		}
		path := filepath.Join(destination, h.Name)
		if h.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return errors.New("release: official archive must contain regular files")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, tr)
		if err := errors.Join(copyErr, f.Close()); err != nil {
			return err
		}
	}
}

func syncOfficialDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
