package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Agent is the local non-model distribution client. Its owner runs Run and
// cancels that context before waiting for shutdown.
type Agent struct {
	Consumer  *Consumer
	HTTP      *http.Client
	ServerURL string
	HostID    string
	Token     string
	mu        sync.Mutex
	target    *protocol.ExtensionTarget
	status    protocol.ExtensionHostStatus
	prepared  map[string]preparedView
	wake      chan struct{}
}
type preparedView struct {
	Status  protocol.ExtensionPrepared
	Binding codexenv.Binding
}

func viewKey(runtime, category string, agent protocol.AgentIdentity) string {
	return runtime + "/" + category + "/" + agent.ExecutionEnvironmentID + "/" + agent.LocalID
}

func (a *Agent) initializeLocked() {
	if a.wake == nil {
		a.wake = make(chan struct{}, 1)
		a.prepared = map[string]preparedView{}
		a.status = protocol.ExtensionHostStatus{Enabled: true, Prepared: []protocol.ExtensionPrepared{}, Adoptions: []protocol.ExtensionAdoption{}}
		for id := range a.Consumer.Config.Runtimes {
			a.status.Runtimes = append(a.status.Runtimes, id)
		}
		slices.Sort(a.status.Runtimes)
	}
}

func (a *Agent) SetTarget(target *protocol.ExtensionTarget) error {
	if target == nil {
		return nil
	}
	if len(target.Release.ID) != 68 || !strings.HasPrefix(target.Release.ID, "ext-") || !ValidHash(target.Release.ID[4:]) || !ValidHash(target.Release.ManifestSHA256) || target.Release.ManifestSize < 1 || target.Release.ManifestSize > 4<<20 {
		return fmt.Errorf("invalid managed extension target")
	}
	for _, p := range []string{target.ManifestPath, target.ContentBasePath} {
		if !strings.HasPrefix(p, "/runner/v1/extensions/") || strings.ContainsAny(p, "?#\\") || strings.HasPrefix(p, "//") || path.Clean(p) != p {
			return fmt.Errorf("invalid managed extension download path")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initializeLocked()
	if a.target != nil && *a.target == *target {
		return nil
	}
	raw, _ := json.Marshal(target)
	if err := atomicFile(filepath.Join(a.Consumer.Config.Directory, "target.json"), raw); err != nil {
		return err
	}
	copy := *target
	a.target = &copy
	a.status.DesiredRelease = target.Release.ID
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return nil
}

func (a *Agent) Status() protocol.ExtensionHostStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initializeLocked()
	raw, _ := json.Marshal(a.status)
	var out protocol.ExtensionHostStatus
	_ = json.Unmarshal(raw, &out)
	return out
}

func (a *Agent) download(ctx context.Context, route string) (io.ReadCloser, error) {
	base, err := url.Parse(a.ServerURL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("invalid configured extension server")
	}
	rel, err := url.Parse(route)
	if err != nil || rel.IsAbs() || rel.Host != "" || rel.RawQuery != "" || rel.Fragment != "" || !strings.HasPrefix(rel.Path, "/runner/v1/extensions/") || path.Clean(rel.Path) != rel.Path || rel.RawPath != "" {
		return nil, fmt.Errorf("extension download must stay on the configured server")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.ResolveReference(rel).String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Tend-Host", a.HostID)
	req.Header.Set("Authorization", "Bearer "+a.Token)
	client := http.DefaultClient
	if a.HTTP != nil {
		client = a.HTTP
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copy.Do(req)
	if err != nil {
		return nil, fmt.Errorf("extension server request failed")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("extension server returned HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (a *Agent) sync(ctx context.Context) error {
	a.mu.Lock()
	a.initializeLocked()
	if a.target == nil {
		a.mu.Unlock()
		return nil
	}
	target := *a.target
	a.mu.Unlock()
	if r, err := a.Consumer.Release(target.Release.ID); err == nil {
		raw, _ := json.Marshal(r)
		if Hash(raw) == target.Release.ManifestSHA256 && int64(len(raw)) == target.Release.ManifestSize {
			a.mu.Lock()
			a.status.DownloadedRelease = r.ID
			a.status.LastError = ""
			a.mu.Unlock()
			return nil
		}
	}
	body, err := a.download(ctx, target.ManifestPath)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(body, 4<<20+1))
	_ = body.Close()
	if err != nil {
		return err
	}
	if Hash(raw) != target.Release.ManifestSHA256 || int64(len(raw)) != target.Release.ManifestSize {
		return fmt.Errorf("extension manifest integrity mismatch")
	}
	var r protocol.ExtensionRelease
	if json.Unmarshal(raw, &r) != nil || r.ID != target.Release.ID {
		return fmt.Errorf("invalid extension manifest")
	}
	if err = a.Consumer.Receive(ctx, r, func(ctx context.Context, res protocol.ExtensionResource) (io.ReadCloser, error) {
		return a.download(ctx, strings.TrimRight(target.ContentBasePath, "/")+"/"+res.ContentSHA256)
	}); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status.DownloadedRelease = r.ID
	a.status.LastError = ""
	return nil
}

func (a *Agent) Run(ctx context.Context) error {
	a.mu.Lock()
	a.initializeLocked()
	wake := a.wake
	a.mu.Unlock()
	if raw, err := os.ReadFile(filepath.Join(a.Consumer.Config.Directory, "target.json")); err == nil {
		var target protocol.ExtensionTarget
		if err = json.Unmarshal(raw, &target); err != nil {
			return err
		}
		if err = a.SetTarget(&target); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := a.sync(syncCtx)
		cancel()
		if err != nil {
			a.mu.Lock()
			a.status.LastError = "release download or validation failed; previous cached content is retained"
			a.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-ticker.C:
		}
	}
}

// PrepareCurrent runs in the existing background reconciler, outside heartbeat.
func (a *Agent) PrepareCurrent(ctx context.Context, runtime, category string, agent protocol.AgentIdentity) error {
	a.mu.Lock()
	a.initializeLocked()
	desired := a.status.DesiredRelease
	downloaded := a.status.DownloadedRelease
	a.mu.Unlock()
	if desired == "" || desired != downloaded {
		return fmt.Errorf("managed extension release is not downloaded")
	}
	r, err := a.Consumer.Release(desired)
	if err != nil {
		return err
	}
	combo, err := Resolve(r, category, agent)
	if err != nil {
		return err
	}
	binding, status, err := a.Consumer.Prepare(ctx, combo.Pin, runtime)
	if err != nil {
		status.Reason = "combination validation failed"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := viewKey(runtime, category, agent)
	view := preparedView{Status: status}
	if binding != nil {
		view.Binding = *binding
	}
	a.prepared[key] = view
	a.status.Prepared = []protocol.ExtensionPrepared{}
	for _, p := range a.prepared {
		a.status.Prepared = append(a.status.Prepared, p.Status)
	}
	return err
}

func (a *Agent) Current(runtime, category string, agent protocol.AgentIdentity) (protocol.ExtensionExecutionPin, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initializeLocked()
	p, ok := a.prepared[viewKey(runtime, category, agent)]
	return p.Status.Pin, ok && p.Status.Ready && a.status.DesiredRelease != "" && p.Status.Pin.ReleaseID == a.status.DesiredRelease
}

func (a *Agent) Binding(ctx context.Context, pin protocol.ExtensionExecutionPin, runtime string) (*codexenv.Binding, error) {
	a.mu.Lock()
	a.initializeLocked()
	prepared := a.prepared[viewKey(runtime, pin.CategoryID, pin.Agent)]
	a.mu.Unlock()
	if prepared.Binding.Extensions == pin && prepared.Binding.Generation != "" {
		binding := prepared.Binding
		return &binding, nil
	}
	return a.Consumer.RetainedBinding(ctx, pin, runtime)
}

func (a *Agent) RefreshAdoptions(ctx context.Context) {
	status, err := a.Consumer.Status(ctx)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initializeLocked()
	a.status.Adoptions = status.Adoptions
	a.status.AdoptionsMore = status.AdoptionsMore
	a.status.SkillExclusions = status.SkillExclusions
}
