package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func (r *Runner) maybeUpgrade(ctx context.Context) {
	if r.cfg.UpdaterControlSocket == "" || r.cfg.UpdaterSocket == "" || r.cfg.UpdaterBundleDir == "" || !r.updaterMu.TryLock() {
		return
	}
	observation, err := r.runtimeObservation(ctx)
	r.controlMu.Lock()
	control := r.control
	r.controlMu.Unlock()
	r.mu.Lock()
	confirmed := r.confirmedRuntime
	r.mu.Unlock()
	if err != nil || observation.Mode != protocol.RunnerReady || control.Upgrade == nil || control.UpgradeID == "" || control.CancelledUpgradeID == control.UpgradeID || confirmed.Mode != protocol.RunnerReady || confirmed.AppliedControlRevision != control.Revision || confirmed.UpgradeID != control.UpgradeID {
		r.updaterMu.Unlock()
		return
	}
	r.wg.Go(func() {
		defer r.updaterMu.Unlock()
		if err := r.downloadAndSubmit(ctx, control); err != nil {
			r.updateUpgradeAttempt(control, func() {
				r.upgradePhase = "download_failed"
				r.upgradeError = "release download or updater confirmation failed"
			})
			r.cfg.Log.WarnContext(ctx, "release download or updater confirmation deferred")
		}
	})
}

func (r *Runner) downloadAndSubmit(ctx context.Context, control protocol.RunnerControl) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	target := control.Upgrade
	if target == nil || !release.ValidSHA(target.ManifestSHA256) || target.ManifestSize <= 0 || target.ManifestSize > release.MaxManifestBytes {
		return errors.New("invalid release target")
	}
	if !r.updateUpgradeAttempt(control, func() { r.upgradePhase = "downloading"; r.downloadedBytes = 0; r.upgradeError = "" }) {
		return errors.New("upgrade attempt changed")
	}
	dir, err := filepath.Abs(filepath.Join(r.cfg.UpdaterBundleDir, target.ManifestSHA256))
	if err != nil {
		return err
	}
	submit := func() error {
		if !r.updateUpgradeAttempt(control, func() {}) {
			return errors.New("upgrade attempt changed")
		}
		if err := r.submitConfiguredUpdater(ctx, control.UpgradeID, dir, target.ManifestSHA256, target.Configuration); err != nil {
			return err
		}
		r.updateUpgradeAttempt(control, func() { r.upgradePhase = "submitted" })
		return nil
	}
	if bundle, loadErr := release.Load(dir); loadErr == nil && bundle.ManifestSHA256 == target.ManifestSHA256 && bundle.Manifest.ReleaseID == control.ReleaseID {
		return submit()
	}
	if _, err := os.Lstat(dir); err == nil {
		return errors.New("existing release bundle is invalid")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(r.cfg.UpdaterBundleDir, 0700); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(r.cfg.UpdaterBundleDir, ".download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err := r.downloadReleaseFile(ctx, target.ManifestPath, filepath.Join(temp, "manifest.json"), target.ManifestSize, target.ManifestSHA256); err != nil {
		return err
	}
	r.updateUpgradeAttempt(control, func() { r.downloadedBytes += target.ManifestSize })
	raw, err := os.ReadFile(filepath.Join(temp, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest release.Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing release manifest")
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if manifest.ReleaseID != control.ReleaseID {
		return errors.New("release identity mismatch")
	}
	if !manifest.Protocol.Contains(protocol.CurrentRunnerProtocol) || !manifest.Storage.RunnerJournal.Contains(journal.CurrentVersion) {
		return errors.New("release does not support runner protocol or journal")
	}
	for _, artifact := range manifest.Artifacts {
		dest := filepath.Join(temp, artifact.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if err := r.downloadReleaseFile(ctx, target.ArtifactBasePath+artifact.SHA256, dest, artifact.Size, artifact.SHA256); err != nil {
			return err
		}
		r.updateUpgradeAttempt(control, func() { r.downloadedBytes += artifact.Size })
	}
	bundle, err := release.Load(temp)
	if err != nil {
		return err
	}
	if bundle.ManifestSHA256 != target.ManifestSHA256 {
		return errors.New("release manifest changed")
	}
	if err := os.Rename(temp, dir); err != nil {
		return err
	}
	parent, err := os.Open(r.cfg.UpdaterBundleDir)
	if err != nil {
		return err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return err
	}
	return submit()
}

func (r *Runner) downloadReleaseFile(ctx context.Context, path, dest string, size int64, digest string) error {
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || relative.Host != "" || relative.User != nil || relative.RawQuery != "" || relative.Fragment != "" || len(path) == 0 || path[0] != '/' {
		return errors.New("release URL must be a server-relative path")
	}
	base, err := url.Parse(r.cfg.ServerURL)
	if err != nil {
		return err
	}
	endpoint := base.ResolveReference(relative)
	if endpoint.Scheme != base.Scheme || endpoint.Host != base.Host {
		return errors.New("release origin changed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.cfg.HostToken)
	request.Header.Set(headerHost, r.cfg.HostID)
	client := *r.c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("release redirects forbidden") }
	response, err := client.Do(request)
	if err != nil {
		return errors.New("release transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("release download refused")
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, size+1))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("release size or digest mismatch")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return nil
}

func (r *Runner) submitUpdater(ctx context.Context, id, dir, digest string) error {
	return r.submitConfiguredUpdater(ctx, id, dir, digest, nil)
}
func (r *Runner) submitConfiguredUpdater(ctx context.Context, id, dir, digest string, configuration *protocol.RunnerConfiguration) error {
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", r.cfg.UpdaterControlSocket)
	}}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("updater redirects forbidden") }}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/status", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	var status struct {
		State struct {
			Terminals map[string]struct {
				Phase string `json:"phase"`
			} `json:"terminals"`
			Operation *struct {
				ID        string `json:"id"`
				Candidate struct {
					ManifestSHA256 string `json:"manifest_sha256"`
				} `json:"candidate"`
			} `json:"operation"`
		} `json:"state"`
		Error string `json:"error"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&status)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || status.Error != "" {
		return errors.New("updater status unavailable")
	}
	// Terminal IDs are durable fences even after Operation has moved to another
	// batch. Never replay an installation already cancelled or completed.
	if terminal, ok := status.State.Terminals[id]; ok {
		switch terminal.Phase {
		case "cancelled", "committed", "rolled_back":
			return nil
		}
	}
	if op := status.State.Operation; op != nil && op.ID == id {
		if op.Candidate.ManifestSHA256 != digest {
			return errors.New("updater operation identity mismatch")
		}
		return nil
	}
	body, _ := json.Marshal(struct {
		Configuration *protocol.RunnerConfiguration `json:"configuration,omitempty"`
		ID            string                        `json:"id"`
		Release       string                        `json:"release"`
	}{configuration, id, dir})
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/upgrade", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err = client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("updater rejected release")
	}
	var result struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if result.Error != "" {
		return errors.New("updater refused release")
	}
	return nil
}

// A late result from an older command cannot poison a resumed attempt.
func (r *Runner) updateUpgradeAttempt(control protocol.RunnerControl, update func()) bool {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	if r.control.Revision != control.Revision || r.control.UpgradeID != control.UpgradeID || r.control.Mode != protocol.RunnerMaintenance {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	update()
	return true
}
