package upgradectl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	mu          sync.Mutex
	commands    []app.Command
	envelopes   []protocol.Envelope
	view        protocol.UpgradeView
	batches     map[string]store.UpgradeRow
	healthCalls int
	observation app.ServerObservation
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
	return c, f
}
func testBundle(t *testing.T) string {
	t.Helper()
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
	return dir
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
