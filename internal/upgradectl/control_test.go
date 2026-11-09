package upgradectl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

type fakeBackend struct {
	preflightErr    error
	preflightResult *protocol.Operation
	preflights      int
	mu              sync.Mutex
	commands        []app.Command
	envelopes       []protocol.Envelope
	view            protocol.UpgradeView
	batches         map[string]store.UpgradeRow
	healthCalls     int
	observation     app.ServerObservation
}

func (f *fakeBackend) CheckUpgradeStart(_ context.Context, _ *protocol.Envelope, _ app.StartUpgrade) (*protocol.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflights++
	return f.preflightResult, f.preflightErr
}
func (f *fakeBackend) Execute(_ context.Context, _ protocol.Principal, e *protocol.Envelope, c app.Command) (protocol.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, c)
	f.envelopes = append(f.envelopes, *e)
	raw, _ := json.Marshal(f.view)
	return protocol.Operation{Result: raw}, nil
}
func (f *fakeBackend) ListCancelledServerUpgrades(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, row := range f.batches {
		if row.View.Status == "cancelled" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (f *fakeBackend) GetUpgrade(_ context.Context, id string) (store.UpgradeRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batches[id], nil
}
func (f *fakeBackend) CurrentUpgrade(context.Context) (protocol.UpgradeView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view, nil
}
func (f *fakeBackend) TickUpgrade(_ context.Context, o app.ServerObservation) (protocol.UpgradeView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observation = o
	return f.view, nil
}
func (f *fakeBackend) ServerUpgradeAuthorize(_ context.Context, b protocol.UpgradeBinding) (protocol.UpgradeGrant, error) {
	return protocol.UpgradeGrant{Binding: b, Allowed: true, RemainingMillis: 1000}, nil
}
func (f *fakeBackend) ServerUpgradeHealth(_ context.Context, b protocol.UpgradeBinding) (protocol.UpgradeHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthCalls++
	return protocol.UpgradeHealth{Binding: b, ServerConfirmed: true, ServerRevision: 1, Healthy: true}, nil
}
func testController(t *testing.T) (*Controller, *fakeBackend) {
	t.Helper()
	root := t.TempDir()
	sum := sha256.Sum256([]byte("server"))
	f := &fakeBackend{view: protocol.UpgradeView{UpgradeID: "batch", Revision: 1, Status: "running", Phase: "server_draining"}}
	c, err := New(Config{Instance: "server", ProcessInstance: "current-process", BinarySHA256: hex.EncodeToString(sum[:]), ControlSocket: filepath.Join(root, "control.sock"), UpdaterSocket: filepath.Join(root, "updater.sock"), ReleaseDir: filepath.Join(root, "releases"), Interval: time.Hour}, f, protocol.NewULIDGen(time.Now, nil), func(context.Context) (app.ServerObservation, error) {
		return app.ServerObservation{Healthy: true, MainDrained: true}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.cfg.InspectExecutableSource = func(string) (release.SourceIdentity, error) {
		return release.SourceIdentity{Commit: strings.Repeat("a", 40)}, nil
	}
	c.cfg.ObserveUpdater = func(context.Context, string) (updater.SelfUpdateStatus, error) {
		return updater.SelfUpdateStatus{Supported: true, Observation: protocol.UpdaterSelfObservation{InstanceID: "server"}}, nil
	}
	return c, f
}
func testBundle(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("complete business release sets target Linux")
	}
	dir := t.TempDir()
	m := release.Manifest{Format: 1, ReleaseID: "release-2", Protocol: release.Range{Min: 1, Max: 2}, Storage: release.Storage{ServerSchema: release.Range{Min: 26, Max: 28}, RunnerJournal: release.Range{Min: 8, Max: 10}}}
	for _, role := range []string{"server", "runner"} {
		body := []byte(role)
		sum := sha256.Sum256(body)
		m.Artifacts = append(m.Artifacts, release.Artifact{Role: role, OS: runtime.GOOS, Arch: runtime.GOARCH, Path: role, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])})
		if err := os.WriteFile(filepath.Join(dir, role), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestCompleteSet(t, dir, m)
	return dir
}

func writeTestCompleteSet(t *testing.T, dir string, m release.Manifest) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	s := release.Set{Format: 2, ReleaseID: m.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: runtime.GOARCH, ManifestSHA256: hex.EncodeToString(h[:]), Artifacts: append([]release.Artifact(nil), m.Artifacts...)}
	for _, role := range []string{"updater", "cli"} {
		body, err := os.ReadFile(filepath.Join(dir, m.Artifacts[0].Path))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(body)
		if err := os.WriteFile(filepath.Join(dir, role), body, 0o600); err != nil {
			t.Fatal(err)
		}
		s.Artifacts = append(s.Artifacts, release.Artifact{Role: role, OS: "linux", Arch: runtime.GOARCH, Path: role, Size: int64(len(body)), SHA256: hex.EncodeToString(h[:])})
	}
	raw, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release-set.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
func TestStartPinsCompleteBundleBeforeApplicationAdmission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux install semantics")
	}
	c, f := testController(t)
	dir := testBundle(t)
	req := protocol.UpgradeStartRequest{RequestID: c.ids.New(protocol.PrefixRequest), UpgradeID: "batch", Release: dir}
	if _, err := c.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 1 {
		t.Fatal("start was not executed")
	}
	start := f.commands[0].(app.StartUpgrade)
	if start.ServerHash == "" || start.ManifestSHA256 == "" || start.ServerInstance != "server" {
		t.Fatalf("missing pinned metadata: %+v", start)
	}
	if *f.envelopes[0].ExpectedControlRevision != 0 || f.envelopes[0].Target.ID != "batch" {
		t.Fatal("request lost revision or batch identity")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	staged, err := release.Load(filepath.Join(c.cfg.ReleaseDir, "batch"))
	if err != nil || staged.ManifestSHA256 != start.ManifestSHA256 {
		t.Fatalf("staged release still depends on caller directory: %v", err)
	}
}
func TestCorruptReleaseNeverStartsApplicationBatch(t *testing.T) {
	c, f := testController(t)
	dir := testBundle(t)
	if err := os.WriteFile(filepath.Join(dir, "runner"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(context.Background(), protocol.UpgradeStartRequest{RequestID: c.ids.New(protocol.PrefixRequest), UpgradeID: "batch", Release: dir}); err == nil {
		t.Fatal("corrupt release accepted")
	}
	if len(f.commands) > 0 {
		t.Fatal("corrupt release reached app")
	}
}

func TestNewUpgradeRequiresCompleteFormatTwoBeforeAdmission(t *testing.T) {
	for _, kind := range []string{"service-only", "format-one", "missing-updater", "modified-source", "wrong-source", "old-updater"} {
		t.Run(kind, func(t *testing.T) {
			c, f := testController(t)
			dir := testBundle(t)
			switch kind {
			case "service-only":
				if err := os.Remove(filepath.Join(dir, "release-set.json")); err != nil {
					t.Fatal(err)
				}
			case "format-one":
				path := filepath.Join(dir, "release-set.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var set release.Set
				if err := json.Unmarshal(raw, &set); err != nil {
					t.Fatal(err)
				}
				set.Format = 1
				raw, _ = json.Marshal(set)
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-updater":
				if err := os.Remove(filepath.Join(dir, "updater")); err != nil {
					t.Fatal(err)
				}
			case "modified-source":
				c.cfg.InspectExecutableSource = func(string) (release.SourceIdentity, error) {
					return release.SourceIdentity{Commit: strings.Repeat("a", 40), Modified: true}, nil
				}
			case "wrong-source":
				c.cfg.InspectExecutableSource = func(string) (release.SourceIdentity, error) {
					return release.SourceIdentity{Commit: strings.Repeat("b", 40)}, nil
				}
			case "old-updater":
				c.cfg.ObserveUpdater = func(context.Context, string) (updater.SelfUpdateStatus, error) {
					return updater.SelfUpdateStatus{}, errors.New("capability missing")
				}
			}
			_, err := c.Start(t.Context(), protocol.UpgradeStartRequest{RequestID: c.ids.New(protocol.PrefixRequest), UpgradeID: "rejected", Release: dir})
			if err == nil || len(f.commands) != 0 {
				t.Fatal("unsupported release reached business admission", err, len(f.commands))
			}
		})
	}
}
func TestOldServerCannotReportCandidateHealth(t *testing.T) {
	c, f := testController(t)
	b := protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: "different-candidate", ArtifactSHA256: c.cfg.BinarySHA256}
	raw, _ := json.Marshal(b)
	w := httptest.NewRecorder()
	c.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/updater/health", bytes.NewReader(raw)))
	if w.Code != http.StatusConflict || f.healthCalls != 0 {
		t.Fatal("old server answered candidate health")
	}
	b.ProcessInstanceID = c.cfg.ProcessInstance
	raw, _ = json.Marshal(b)
	w = httptest.NewRecorder()
	c.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/updater/health", bytes.NewReader(raw)))
	if w.Code != http.StatusOK || f.healthCalls != 1 {
		t.Fatal("matching candidate health unavailable")
	}
}
func TestTickRequestsOnlyReadyServerAndUsesStableBatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix transport")
	}
	c, f := testController(t)
	listener, err := net.Listen("unix", c.cfg.UpdaterSocket)
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan updater.ControlRequest, 2)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: updater.State{InstanceID: c.cfg.Instance}})
			return
		}
		if r.URL.Path != "/upgrade" {
			t.Error("unexpected updater path")
		}
		var req updater.ControlRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		calls <- req
		_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: updater.State{InstanceID: c.cfg.Instance}})
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
		t.Fatal("draining server was replaced")
	default:
	}
	f.mu.Lock()
	f.view.Phase = "server_ready"
	f.mu.Unlock()
	for i := 0; i < 2; i++ {
		if err := c.tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := <-calls
		if r.ID != "batch" || r.Release != filepath.Join(c.cfg.ReleaseDir, "batch") {
			t.Fatal("unstable installation identity")
		}
	}
}
func TestControlRunShutsDownOwnedSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix transport")
	}
	c, _ := testController(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(c.cfg.ControlSocket); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller goroutine leaked")
	}
	if _, err := os.Stat(c.cfg.ControlSocket); !os.IsNotExist(err) {
		t.Fatal("controller socket remains")
	}
}

func TestServerReadinessCannotBeReplacedByHistoricalHealth(t *testing.T) {
	c, _ := testController(t)
	c.observe = func(context.Context) (app.ServerObservation, error) {
		return app.ServerObservation{Healthy: true, MainDrained: false}, nil
	}
	b := protocol.UpgradeBinding{InstanceID: c.cfg.Instance, ProcessInstanceID: c.cfg.ProcessInstance, ArtifactSHA256: c.cfg.BinarySHA256}
	raw, _ := json.Marshal(b)
	w := httptest.NewRecorder()
	c.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/updater/health", bytes.NewReader(raw)))
	var health protocol.UpgradeHealth
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &health) != nil || health.Healthy {
		t.Fatal("historical health bypassed current native readiness")
	}
}

func TestDeliveryStartReadsOnlyPinnedExecutableSourceAndRetainsTaskIdentity(t *testing.T) {
	c, f := testController(t)
	c.cfg.InspectExecutableSource = release.ExecutableSource
	dir := testBundle(t)
	request := protocol.UpgradeStartRequest{RequestID: c.ids.New(protocol.PrefixRequest), UpgradeID: "batch", Release: dir, DeliveryTaskID: "approved-delivery"}
	if _, err := c.Start(t.Context(), request); err == nil {
		t.Fatal("non-executable delivery bundle reached application")
	}
	if len(f.commands) != 0 {
		t.Fatal("invalid source was admitted")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	source, err := release.ExecutableSource(executable)
	if err != nil {
		t.Fatal(err)
	}
	source.Commit = strings.Repeat("a", 40)
	source.Modified = false
	c.cfg.InspectExecutableSource = func(string) (release.SourceIdentity, error) { return source, nil }
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	// Use a separate batch: immutable staging from the rejected request remains
	// evidence and is not overwritten by another release under its old identity.
	request.UpgradeID = "verifiable-batch"
	request.RequestID = c.ids.New(protocol.PrefixRequest)
	var manifest release.Manifest
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Artifacts {
		a := &manifest.Artifacts[index]
		a.Size = int64(len(body))
		a.SHA256 = hash
		if err := os.WriteFile(filepath.Join(dir, a.Path), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	writeTestCompleteSet(t, dir, manifest)
	if _, err := c.Start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 1 {
		t.Fatal("verifiable bundle not forwarded")
	}
	start := f.commands[0].(app.StartUpgrade)
	if start.DeliveryTaskID != request.DeliveryTaskID || start.ArtifactSources[hash] != source || start.ServerHash != hash {
		t.Fatal("pinned source/task identity lost", start)
	}
	var payload map[string]any
	if err := json.Unmarshal(f.envelopes[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["delivery_task_id"] != request.DeliveryTaskID {
		t.Fatal("dedup identity omits delivery authority", payload)
	}
}

func TestUpgradeManagerParkPreservesCompleteAdmissionHash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local bundle copy")
	}
	c, f := testController(t)
	req := protocol.UpgradeStartRequest{RequestID: c.ids.New(protocol.PrefixRequest), UpgradeID: "park-hash", Release: testBundle(t)}
	if _, err := c.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	first := f.envelopes[0]
	var payload struct {
		ReleaseSetSHA256 string                                  `json:"release_set_sha256,omitempty"`
		DeliveryTaskID   string                                  `json:"delivery_task_id,omitempty"`
		ReleaseID        string                                  `json:"release_id"`
		ManifestSHA256   string                                  `json:"manifest_sha256"`
		Configurations   map[string]protocol.RunnerConfiguration `json:"configurations,omitempty"`
	}
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(payload)
	if string(first.Payload) != string(legacy) {
		t.Fatal("old canonical input changed", string(first.Payload), string(legacy))
	}
	expected := first
	expected.Payload = legacy
	oldHash, _ := protocol.PayloadHash(&expected)
	gotHash, _ := protocol.PayloadHash(&first)
	if oldHash != gotHash {
		t.Fatal("hash changed")
	}
	// The exact old request can be replayed without enabling the opt-in.
	if _, err := c.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	replayHash, _ := protocol.PayloadHash(&f.envelopes[1])
	if replayHash != oldHash || f.commands[1].(app.StartUpgrade).ParkIdleManagers {
		t.Fatal("legacy retry changed intent")
	}
	c.cfg.InspectServerCapabilities = func(_ context.Context, _ string, sha string) (release.ServerCapabilities, error) {
		return release.ServerCapabilities{BinarySHA256: sha, Capabilities: []string{release.ServerCapabilityManagerPark}}, nil
	}
	req.ParkIdleManagers = true
	req.RequestID = c.ids.New(protocol.PrefixRequest)
	if _, err := c.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if !f.commands[2].(app.StartUpgrade).ParkIdleManagers || !bytes.Contains(f.envelopes[2].Payload, []byte(`"park_idle_managers":true`)) {
		t.Fatal("opt-in not admitted")
	}
}

func TestUpgradeManagerParkRejectsOldCandidateBeforeAdmission(t *testing.T) {
	c, f := testController(t)
	req := protocol.UpgradeStartRequest{ParkIdleManagers: true, RequestID: c.ids.New(protocol.PrefixRequest), UpgradeID: "unsupported-park", Release: testBundle(t)}
	called := 0
	c.cfg.InspectServerCapabilities = func(_ context.Context, _ string, sha string) (release.ServerCapabilities, error) {
		called++
		return release.ServerCapabilities{BinarySHA256: sha}, nil
	}
	if _, err := c.Start(t.Context(), req); err == nil || len(f.commands) != 0 || called != 1 {
		t.Fatal("unsupported candidate admitted", len(f.commands), called, err)
	}
	// The exact same old bundle still follows its original non-opt-in path.
	req.ParkIdleManagers = false
	req.RequestID = c.ids.New(protocol.PrefixRequest)
	if _, err := c.Start(t.Context(), req); err != nil || len(f.commands) != 1 || called != 1 {
		t.Fatal("legacy start changed", len(f.commands), called, err)
	}
}

func TestUpgradeManagerParkRejectsUnauthorizedProbeBeforeExecution(t *testing.T) {
	c, f := testController(t)
	f.preflightErr = protocol.Errorf(protocol.CodeForbidden, "delivery not approved")
	probes := 0
	c.cfg.InspectServerCapabilities = func(context.Context, string, string) (release.ServerCapabilities, error) {
		probes++
		return release.ServerCapabilities{}, nil
	}
	req := protocol.UpgradeStartRequest{ParkIdleManagers: true, RequestID: c.ids.New(protocol.PrefixRequest), UpgradeID: "unauthorized-probe", Release: testBundle(t)}
	if _, err := c.Start(t.Context(), req); protocol.CodeOf(err) != protocol.CodeForbidden || probes != 0 || len(f.commands) != 0 || f.preflights != 1 {
		t.Fatal("candidate executed before authority", err, probes, len(f.commands), f.preflights)
	}
	f.preflightErr = nil
	raw, _ := json.Marshal(protocol.UpgradeView{UpgradeID: req.UpgradeID, Revision: 2, Status: "running", Phase: "server_draining"})
	f.preflightResult = &protocol.Operation{RequestID: req.RequestID, Status: protocol.OpSucceeded, Result: raw}
	view, err := c.Start(t.Context(), req)
	if err != nil || view.UpgradeID != req.UpgradeID || probes != 0 || len(f.commands) != 0 {
		t.Fatal("recorded replay probed or admitted again", view, err, probes)
	}
}
