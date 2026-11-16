package runner

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// Exercise the real control/readiness producer and RPC path. Only the updater
// process and server replacement health are external fixture boundaries; this
// test stops at the first authorized runner drain, without installing a binary.
func TestUpgradeCutoverAcceptsActualReadyRunnerHeartbeats(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 31)
	db, err := wireLedgerFixture.Open(ctx, filepath.Join(t.TempDir(), "server.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := app.New(db, ids, clock.Now)
	execute := func(target protocol.Target, payload any, cmd app.Command, rev *int64) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: target, Payload: raw, ExpectedControlRevision: rev}, cmd)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(cmd.Operation(), op, err)
		}
	}
	hosts := []string{"cutover-a", "cutover-b"}
	config := app.ConfigPayload{Repos: []app.RepoConfig{{Repo: "repo", RemoteURL: "https://example.com/repo.git"}}}
	for _, host := range hosts {
		config.Hosts = append(config.Hosts, app.HostConfig{HostID: host, OS: "linux", Arch: "amd64", Slots: 1, Repos: []string{"repo"}, TokenHash: runnerapi.HashToken("test-host-secret"), Nodes: []string{"tag:tend-runner"}})
	}
	execute(protocol.Target{Type: "config"}, config, app.SyncConfig{}, nil)
	api := runnerapi.New(a, netx.Static{Identity: &netx.Identity{Node: "test-node", Tags: []string{"tag:tend-runner"}}}, []byte("test-server-secret"), runnerapi.Config{}, slog.New(slog.DiscardHandler))
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	var fleet []*Runner
	for _, host := range hosts {
		j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		defer j.Close()
		// These declared updater locations intentionally have no process: no
		// installation is driven by this control/heartbeat regression.
		socket := testkit.SocketPath(t, "absent-updater.sock")
		r := New(Config{Journal: j, HostID: host, HostToken: "test-host-secret", ServerURL: server.URL, HTTPClient: server.Client(), InstanceID: "original-" + host, Build: protocol.RunnerBuild{BinarySHA256: strings.Repeat("a", 64), Version: "old"}, UpdaterSocket: socket, UpdaterControlSocket: socket, UpdaterBundleDir: t.TempDir(), Now: clock.Now, IDs: ids, Log: slog.New(slog.DiscardHandler)})
		if err := r.hello(ctx); err != nil {
			t.Fatal("initial real hello", err)
		}
		fleet = append(fleet, r)
	}
	var schema int64
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema)
	}); err != nil {
		t.Fatal(err)
	}
	candidate := app.StartUpgrade{Manifest: release.Manifest{Format: 1, ReleaseID: "ready-cutover-release", Protocol: release.Range{Min: protocol.PreviousRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: schema, Max: schema}, RunnerJournal: release.Range{Min: journal.CurrentVersion, Max: journal.CurrentVersion}}, Artifacts: []release.Artifact{{Role: "server", OS: "linux", Arch: "amd64", Path: "server", Size: 20, SHA256: strings.Repeat("c", 64)}, {Role: "runner", OS: "linux", Arch: "amd64", Path: "runner", Size: 10, SHA256: strings.Repeat("b", 64)}}}, ManifestSHA256: strings.Repeat("d", 64), ManifestSize: 128, ServerHash: strings.Repeat("c", 64), ServerInstance: "server"}
	zero := int64(0)
	execute(protocol.Target{Type: "upgrade", ID: "ready-cutover"}, map[string]string{"release_id": candidate.Manifest.ReleaseID}, candidate, &zero)
	tick := func(observation app.ServerObservation) protocol.UpgradeView {
		t.Helper()
		v, err := a.TickUpgrade(ctx, observation)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := tick(app.ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)}); v.Phase != "server_ready" {
		t.Fatal("server did not request actual maintenance ACKs", v)
	}
	for _, r := range fleet {
		r.heartbeat(ctx) // Receive the actual maintenance control.
		if err := r.maintainWorkspaces(ctx); err != nil {
			t.Fatal(err)
		}
		observation, err := r.runtimeObservation(ctx)
		if err != nil || observation.Mode != protocol.RunnerReady || len(observation.Blockers) != 0 {
			t.Fatal("idle maintenance did not become ready", observation, err)
		}
		r.heartbeat(ctx) // Send ready, rather than replacing it with maintenance.
	}
	binding := protocol.UpgradeBinding{UpgradeID: "ready-cutover", ReleaseID: candidate.Manifest.ReleaseID, ManifestSHA256: candidate.ManifestSHA256, ArtifactSHA256: candidate.ServerHash, InstanceID: "server", ProcessInstanceID: "new-server"}
	if grant, err := a.ServerUpgradeAuthorize(ctx, binding); err != nil || !grant.Allowed {
		t.Fatal("actual ready ACKs did not authorize replacement", grant, err)
	}
	before, err := a.GetUpgrade(ctx, "ready-cutover")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(70 * time.Second)
	if _, err := a.SweepRunnerPresence(ctx); err != nil {
		t.Fatal(err)
	}
	healthy := app.ServerObservation{InstanceID: "server", ProcessInstanceID: binding.ProcessInstanceID, BinarySHA256: candidate.ServerHash, MainDrained: true, Healthy: true}
	tick(healthy)
	clock.Advance(5 * time.Second)
	if v := tick(healthy); v.Status != "running" || v.Phase != "runners" {
		t.Fatal("actual ready cohort rejected on restoring normal", v)
	}
	if v := tick(app.ServerObservation{}); v.Status != "running" || v.Hosts[0].Status != "queued" || v.Hosts[1].Status != "queued" {
		t.Fatal("old ready ACK rejected while waiting for actual normal", v)
	}
	for i, r := range fleet {
		r.heartbeat(ctx) // Send retained ready and receive the new normal control.
		r.heartbeat(ctx) // Produce and send the actual new normal acknowledgment.
		if v := tick(app.ServerObservation{}); v.Status != "running" || i == 0 && v.Hosts[0].Status != "queued" || i == 1 && v.Hosts[0].Phase != "draining" {
			t.Fatal("actual normal ACKs did not gate the first runner drain", i, v)
		}
	}
	after, err := a.GetUpgrade(ctx, "ready-cutover")
	if err != nil || after.View.OverallDeadlineMillis != before.View.OverallDeadlineMillis {
		t.Fatal("actual recovery extended the original batch deadline", after, err)
	}
}
