package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/extensions"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func controlRunner(t *testing.T) *Runner {
	t.Helper()
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return New(Config{Journal: j, InstanceID: "instance", Now: clock.Now})
}
func TestMaintenanceFixedGrantAndRestartConfirmation(t *testing.T) {
	r := controlRunner(t)
	ctx := context.Background()
	sent := r.cfg.Now()
	command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "upgrade", Authorization: &protocol.MaintenanceAuthorization{ID: "grant", DeadlineMillis: sent.Add(300 * time.Second).UnixMilli(), RemainingMillis: 300000, RunIDs: []string{"run"}}}
	if err := r.applyControl(ctx, &command, sent, true); err != nil {
		t.Fatal(err)
	}
	first := r.authorizedDeadline("run", time.Time{})
	if !first.Equal(sent.Add(225 * time.Second)) {
		t.Fatalf("deadline %v", first)
	}
	if err := r.applyControl(ctx, &command, sent.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if !r.authorizedDeadline("run", time.Time{}).Equal(first) {
		t.Fatal("repeated authorization extended deadline")
	}
	renewed := protocol.RunnerControl{Revision: 2, Mode: protocol.RunnerNormal}
	if err := r.applyControl(ctx, &renewed, sent, false); err != nil {
		t.Fatal(err)
	}
	if err := r.applyControl(ctx, &renewed, sent, false); err != nil {
		t.Fatal(err)
	}
	if !r.authorizedDeadline("run", time.Time{}).Equal(first) {
		t.Fatal("ordinary control revoked grant")
	}
	restarted := New(r.cfg)
	if err := restarted.loadControl(ctx); err != nil {
		t.Fatal(err)
	}
	if !restarted.authorizedDeadline("run", time.Time{}).IsZero() {
		t.Fatal("persisted grant active before hello")
	}
	if err := restarted.applyControl(ctx, &renewed, sent, false); err != nil {
		t.Fatal(err)
	}
	if !restarted.authorizedDeadline("run", time.Time{}).IsZero() {
		t.Fatal("heartbeat activated old authorization")
	}
}
func TestMaintenanceReadinessRetainsUnconfirmedWork(t *testing.T) {
	r := controlRunner(t)
	ctx := t.Context()
	command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}
	if err := r.applyControl(ctx, &command, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	r.resourceReconciled = true
	view, err := r.runtimeObservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != protocol.RunnerReady {
		t.Fatalf("not ready: %+v", view)
	}
	env := protocol.Envelope{RequestID: "claim", Operation: protocol.OpRunnerClaim}
	if err := r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: env}); err != nil {
		t.Fatal(err)
	}
	view, err = r.runtimeObservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == protocol.RunnerReady || view.PendingClaims != 1 {
		t.Fatalf("false ready: %+v", view)
	}
	if err := r.cfg.Journal.DeleteRequest(ctx, "claim"); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.AcceptAssignment(ctx, protocol.Assignment{RunID: "held", Protocol: 0}, ""); err != nil {
		t.Fatal(err)
	}
	view, err = r.runtimeObservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == protocol.RunnerReady {
		t.Fatal("open run ready")
	}
}
func TestControlCannotReuseRevisionOrGrantIdentity(t *testing.T) {
	r := controlRunner(t)
	ctx := t.Context()
	a := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}
	if err := r.applyControl(ctx, &a, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	a.Mode = protocol.RunnerNormal
	if err := r.applyControl(ctx, &a, r.cfg.Now(), false); err == nil {
		t.Fatal("revision reused")
	}
}

func TestIdleHeartbeatReportsRuntimeAndAppliesMaintenance(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.Default()
	r.c.log = r.cfg.Log
	var seen protocol.HeartbeatPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation != protocol.OpRunnerHeartbeat {
			t.Errorf("operation %s", env.Operation)
		}
		if err := json.Unmarshal(env.Payload, &seen); err != nil {
			t.Error(err)
		}
		result, _ := json.Marshal(protocol.HeartbeatResult{Control: &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}})
		json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: result}})
	}))
	defer server.Close()
	r.c.base = server.URL
	r.c.http = server.Client()
	r.heartbeat(t.Context())
	if seen.Runtime == nil || seen.Runtime.Sequence != 1 || seen.Runtime.InstanceID != "instance" || len(seen.Runs) != 0 {
		t.Fatalf("idle runtime missing: %+v", seen)
	}
	if !r.maintenance() {
		t.Fatal("idle control not applied")
	}
	persisted, err := r.cfg.Journal.Control(t.Context())
	if err != nil || persisted.Revision != 1 {
		t.Fatalf("not durable: %+v %v", persisted, err)
	}
}

func TestMaintenanceDoesNotMakeNewClaims(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.Default()
	r.cfg.RetryDelay = time.Millisecond
	command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}
	if err := r.applyControl(t.Context(), &command, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.claims(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	<-done
	requests, err := r.cfg.Journal.Requests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("new claims during maintenance: %+v", requests)
	}
}

func TestMaintenanceAuthorizationConfiguredCeiling(t *testing.T) {
	for _, remaining := range []int64{900000, 900001} {
		r := controlRunner(t)
		command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, Authorization: &protocol.MaintenanceAuthorization{ID: "grant", DeadlineMillis: r.cfg.Now().Add(15 * time.Minute).UnixMilli(), RemainingMillis: remaining}}
		err := r.applyControl(t.Context(), &command, r.cfg.Now(), true)
		if (err == nil) != (remaining <= 900000) {
			t.Fatalf("remaining %d: %v", remaining, err)
		}
	}
}

func TestMaintenanceAcknowledgesClockIndependentBoundAndKeepsFirstConfirmation(t *testing.T) {
	for _, offset := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			r := controlRunner(t)
			serverTime := time.Unix(200000, 0)
			sent := serverTime.Add(offset)
			now := sent.Add(1500*time.Millisecond + 500*time.Microsecond)
			r.cfg.Now = func() time.Time { return now }
			g := &protocol.MaintenanceAuthorization{ID: "fixed", DeadlineMillis: serverTime.Add(300001 * time.Millisecond).UnixMilli(), RemainingMillis: 300001, RunIDs: []string{"active"}}
			c := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, Authorization: g}
			if err := r.applyControl(t.Context(), &c, sent, true); err != nil {
				t.Fatal(err)
			}
			expectedBound := g.DeadlineMillis - 75001 - 1501
			view, err := r.runtimeObservation(t.Context())
			if err != nil || view.AuthorizationDeadlineMillis != expectedBound {
				t.Fatalf("offset bound %+v want %d err %v", view, expectedBound, err)
			}
			first := r.authorizedDeadline("active", time.Time{})
			if !first.Equal(sent.Add(225000 * time.Millisecond)) {
				t.Fatal("local conservative rounding", first)
			}
			// A duplicate delivered much later must neither revoke already issued
			// protection nor extend it, even with a smaller server remaining time.
			next := *g
			next.RemainingMillis = 100000
			c.Authorization = &next
			laterSent := now.Add(time.Second)
			now = laterSent.Add(90 * time.Second)
			if err := r.applyControl(t.Context(), &c, laterSent, false); err != nil {
				t.Fatal(err)
			}
			view, err = r.runtimeObservation(t.Context())
			if err != nil || view.AuthorizationDeadlineMillis != expectedBound || !r.authorizedDeadline("active", time.Time{}).Equal(first) {
				t.Fatal("duplicate changed confirmed protection", view, err)
			}
		})
	}
}

func TestMaintenanceLongResponseHasNoFalseRestartCoverage(t *testing.T) {
	r := controlRunner(t)
	sent := time.Unix(500000, 0)
	now := sent.Add(230 * time.Second)
	r.cfg.Now = func() time.Time { return now }
	g := &protocol.MaintenanceAuthorization{ID: "late", DeadlineMillis: 1000000, RemainingMillis: 300000, RunIDs: []string{"run"}}
	if err := r.applyControl(t.Context(), &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, Authorization: g}, sent, true); err != nil {
		t.Fatal(err)
	}
	view, err := r.runtimeObservation(t.Context())
	if err != nil || view.AuthorizationDeadlineMillis != 695000 {
		t.Fatalf("long RTT bound %+v %v", view, err)
	}
	if r.authorizedDeadline("run", time.Time{}).After(now) {
		t.Fatal("slow response created future local protection")
	}
}

func TestRunnerAdvertisesOriginalTaskInputSupport(t *testing.T) {
	r := controlRunner(t)
	view, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(view.Capabilities, protocol.CapabilityWorkerObservation) {
		t.Fatalf("worker observation capability missing: %+v", view.Capabilities)
	}
	if !slices.Contains(view.Capabilities, protocol.CapabilityTaskInputs) {
		t.Fatalf("task inputs capability missing: %+v", view.Capabilities)
	}
}

func TestConfigurationDigestRequiresExplicitServerTarget(t *testing.T) {
	r := controlRunner(t)
	r.cfg.ConfigurationSHA256 = "loaded-configuration"
	r.resourceReconciled = true
	view, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "configuration_sha256") {
		t.Fatal("legacy server received an unnegotiated field")
	}
	r.control.Upgrade = &protocol.RunnerUpgradeTarget{Configuration: &protocol.RunnerConfiguration{SHA256: "requested"}}
	view, err = r.runtimeObservation(t.Context())
	if err != nil || view.ConfigurationSHA256 != "loaded-configuration" {
		t.Fatal("reported requested hash instead of actual loaded configuration", view, err)
	}
	r.control.Upgrade = nil
	view, err = r.runtimeObservation(t.Context())
	if err != nil || view.ConfigurationSHA256 != "" {
		t.Fatal("configuration extension leaked after maintenance", view, err)
	}
}

func TestRepositoryObservationsRequireServerNegotiation(t *testing.T) {
	r := controlRunner(t)
	r.cfg.RepositoryBindings = func() []protocol.RunnerRepository {
		return []protocol.RunnerRepository{{Repo: "new", RemoteIdentity: "example.com/new"}}
	}
	before, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"repositories"`) {
		t.Fatal("new field leaked to an old server", string(raw))
	}
	if !slices.Contains(before.Capabilities, protocol.CapabilityRepositoryBindings) {
		t.Fatal("capability was not offered")
	}
	r.controlMu.Lock()
	r.repositoryBindingsSupported = true
	r.controlMu.Unlock()
	after, err := r.runtimeObservation(t.Context())
	if err != nil || len(after.Repositories) != 1 {
		t.Fatal("negotiated bindings missing", after, err)
	}
}

func TestManagedExtensionMetadataRequiresServerNegotiation(t *testing.T) {
	r := controlRunner(t)
	r.cfg.Extensions = &extensions.Agent{Consumer: &extensions.Consumer{Config: extensions.Configuration{Directory: t.TempDir(), Runtimes: map[string]extensions.Runtime{"native": {Home: t.TempDir()}}}}}
	before, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	legacy := reflect.TypeFor[protocol.RunnerRuntime]()
	fields := []reflect.StructField{}
	for i := range legacy.NumField() {
		field := legacy.Field(i)
		if field.Name != "Extensions" {
			fields = append(fields, field)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(reflect.New(reflect.StructOf(fields)).Interface()); err != nil {
		t.Fatal("initial hello cannot be decoded by the previous server schema", err)
	}
	if before.Extensions != nil || !slices.Contains(before.Capabilities, protocol.CapabilityManagedExtensions) {
		t.Fatal("initial capability offer was not metadata-free")
	}
	r.controlMu.Lock()
	r.managedExtensionsSupported = true
	r.controlMu.Unlock()
	after, err := r.runtimeObservation(t.Context())
	if err != nil || after.Extensions == nil {
		t.Fatal("acknowledged server did not receive metadata", err)
	}
}

func TestManagedPublicationWakesIdleReadinessWithoutControlRevisionChange(t *testing.T) {
	r := controlRunner(t)
	r.cfg.Extensions = &extensions.Agent{Consumer: &extensions.Consumer{Config: extensions.Configuration{Directory: t.TempDir()}}}
	for i := range 2 {
		id := strings.Repeat(string(rune('a'+i)), 64)
		target := &protocol.ExtensionTarget{Release: protocol.ExtensionReleaseRef{ID: "ext-" + id, ManifestSHA256: id, ManifestSize: 1}, ManifestPath: "/runner/v1/extensions/ext-" + id + "/manifest", ContentBasePath: "/runner/v1/extensions/ext-" + id + "/content"}
		next := &protocol.RunnerControl{Mode: protocol.RunnerNormal, Extensions: target}
		if err := r.applyControl(t.Context(), next, r.cfg.Now(), false); err != nil {
			t.Fatal(err)
		}
		select {
		case <-r.reconcile:
		default:
			t.Fatal("publication left an idle runner asleep")
		}
		if err := r.applyControl(t.Context(), next, r.cfg.Now(), false); err != nil {
			t.Fatal(err)
		}
		select {
		case <-r.reconcile:
			t.Fatal("unchanged publication caused a reconciliation loop")
		default:
		}
	}
}

func TestMaintenanceObservationKeepsOriginalDelegatedDeadline(t *testing.T) {
	r := controlRunner(t)
	r.cfg.Log = slog.Default()
	r.maintenanceCoverageSupported = true
	clock := testkit.NewClock()
	r.cfg.Now = clock.Now
	ctx := t.Context()
	worker, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	const delegated = "run_01KDVDNA0048R1ZE6R55WDNW1N"
	const renewable = "run_01KDVDNA0048R1ZE6R55WDNW1P"
	r.active[delegated] = &active{as: protocol.Assignment{RunID: delegated, Protocol: protocol.CurrentRunnerProtocol}, ctx: worker, cancel: cancel}
	previous := &protocol.MaintenanceAuthorization{
		ID: "previous", DeadlineMillis: clock.Now().Add(time.Minute).UnixMilli(), RemainingMillis: 60000,
		RunIDs: []string{delegated},
	}
	later := &protocol.MaintenanceAuthorization{
		ID: "later", DeadlineMillis: clock.Now().Add(6 * time.Minute).UnixMilli(), RemainingMillis: 360000,
		RunIDs: []string{renewable},
	}
	for i, ctl := range []protocol.RunnerControl{
		{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "first", Authorization: previous},
		{Revision: 2, Mode: protocol.RunnerNormal},
		{Revision: 3, Mode: protocol.RunnerMaintenance, UpgradeID: "second", Authorization: later},
	} {
		if err := r.applyControl(ctx, &ctl, clock.Now(), i == 0); err != nil {
			t.Fatal(err)
		}
	}
	observation, err := r.runtimeObservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := clock.Now().Add(45 * time.Second).UnixMilli()
	if observation.AuthorizationID != later.ID || observation.MaintenanceCoverage == nil ||
		len(observation.MaintenanceCoverage.RunDeadlines) != 1 || observation.MaintenanceCoverage.RunDeadlines[delegated] != want {
		t.Fatal("unrelated new grant hid the original cutoff", observation)
	}
	r.setTTL(200)
	clock.Advance(46 * time.Second)
	done := make(chan struct{})
	stopCtx, stop := context.WithCancel(ctx)
	go func() {
		defer close(done)
		r.watchdog(stopCtx)
	}()
	defer func() { stop(); <-done }()
	select {
	case <-worker.Done():
		if context.Cause(worker) != errFenced {
			t.Fatal(context.Cause(worker))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not observe the deadline that the runtime reported")
	}
}

func TestHeartbeatLearnsMaintenanceCoverageOnExistingConnection(t *testing.T) {
	r := controlRunner(t)
	r.runnerProtocol = protocol.CurrentRunnerProtocol
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.Default()
	r.c.log = r.cfg.Log
	support := make(chan bool, 1)
	sent := make(chan bool, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(request.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch env.Operation {
		case protocol.OpRunnerHello:
			var hello protocol.HelloPayload
			if err := json.Unmarshal(env.Payload, &hello); err != nil || hello.Runtime.MaintenanceCoverage != nil {
				t.Error("Hello must retain its older wire shape", err)
			}
			result = protocol.HelloResult{LeaseTTLMillis: 60000, ProtocolMax: protocol.CurrentRunnerProtocol}
		case protocol.OpRunnerHeartbeat:
			var heartbeat protocol.HeartbeatPayload
			if err := json.Unmarshal(env.Payload, &heartbeat); err != nil ||
				!slices.Contains(heartbeat.Runtime.Capabilities, protocol.CapabilityRunnerMaintenanceCoverage) {
				t.Error("runner did not advertise maintenance coverage", err)
				return
			}
			sent <- heartbeat.Runtime.MaintenanceCoverage != nil
			negotiated := <-support
			result = protocol.HeartbeatResult{LeaseTTLMillis: 60000, MaintenanceCoverage: negotiated}
		default:
			t.Errorf("unexpected operation %s", env.Operation)
		}
		data, _ := json.Marshal(result)
		if err := json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: data}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	r.c.base, r.c.http = server.URL, server.Client()
	ctx := t.Context()
	if err := r.helloLocked(ctx); err != nil {
		t.Fatal(err)
	}
	before := r.instanceID
	for _, state := range []struct{ support, sent bool }{{false, false}, {true, false}, {true, true}} {
		support <- state.support
		r.heartbeat(ctx)
		select {
		case got := <-sent:
			if got != state.sent {
				t.Fatal("wrong evidence for server negotiation", state, got)
			}
		default:
			t.Fatal("no heartbeat reached the server")
		}
	}
	if r.instanceID != before || !r.maintenanceCoverageSupported {
		t.Fatal("successor heartbeat changed runner identity or failed to negotiate")
	}
}
