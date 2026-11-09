package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestBoundedStartupStartsFactLoopsOnceAcrossAdmissionRelease(t *testing.T) {
	for _, mode := range []string{"ordinary", "initial_hello_release", "waiting_release"} {
		t.Run(mode, func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.Log = slog.New(slog.DiscardHandler)
			r.c.log = r.cfg.Log
			r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 17)
			r.cfg.Build.BinarySHA256 = strings.Repeat("a", 64)
			r.cfg.HostID, r.c.hostID = "original-host", "original-host"
			r.cfg.RetryDelay = time.Millisecond
			original := protocol.Envelope{RequestID: "original-usage", Operation: protocol.OpRunnerUsageUpload,
				Target: protocol.Target{Type: "host", ID: r.cfg.HostID}, Payload: json.RawMessage(`{"run_id":"historical-run","coverage":"syncing"}`)}
			if err := r.cfg.Journal.PutRequest(t.Context(), journal.Request{Envelope: original}); err != nil {
				t.Fatal(err)
			}
			// Retained unknown source metadata must not be reset when the loop
			// resumes. The missing file keeps its existing cursor unresolved.
			path := filepath.Join(t.TempDir(), "unavailable-history.jsonl")
			src := nativeusage.Source{Path: path, Provider: "codex", ThreadID: "original-thread", SessionID: "original-session"}
			source := journal.UsageSource{ID: "original-source", RunID: "historical-run", RegistrationQueued: true, Coverage: "syncing",
				Source: src, Files: []nativeusage.Source{src}, DiscoveredAt: r.cfg.Now().UnixMilli(),
				Cursors: map[string]nativeusage.Cursor{path: {Offset: 42, Anchor: "original-anchor", LastModel: "original-model"}}}
			if err := r.cfg.Journal.RegisterUsageSource(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-force", Role: "runner", Target: protocol.UpgradeBinding{UpgradeID: "original-upgrade",
				InstanceID: "fixed-updater", ProcessInstanceID: r.cfg.InstanceID, ReleaseID: "original-release", ManifestSHA256: strings.Repeat("b", 64), ArtifactSHA256: r.cfg.Build.BinarySHA256}}
			if mode != "ordinary" {
				if mode == "waiting_release" {
					as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "original-manager", LeaseID: "original-lease", Role: protocol.RoleIssueManager}
					if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, ""); err != nil {
						t.Fatal(err)
					}
					fact := journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-pending-event", Operation: protocol.OpManagerNativeEvent,
						Target: runTarget(as), Payload: json.RawMessage(`{"sequence":1,"body":{}}`)}}
					if err := r.cfg.Journal.PutRequest(t.Context(), fact); err != nil {
						t.Fatal(err)
					}
				}
				bindRunnerBoundedTestAuthority(t, r.cfg.Journal, &authority)
				if err := r.cfg.Journal.QuarantineBoundedRecovery(t.Context(), authority); err != nil {
					t.Fatal(err)
				}
			}
			var hellos, events, uploads atomic.Int64
			waiting := make(chan struct{}, 1)
			claimed := make(chan struct{}, 1)
			uploaded := make(chan protocol.Envelope, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				result := protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}
				switch env.Operation {
				case protocol.OpRunnerHello:
					var input protocol.HelloPayload
					if err := json.Unmarshal(env.Payload, &input); err != nil || input.Runtime == nil {
						t.Error("missing actual startup runtime", err)
						return
					}
					first := hellos.Add(1) == 1
					hello := protocol.HelloResult{NativeUsage: true, LeaseTTLMillis: 60000}
					if mode == "initial_hello_release" || mode == "waiting_release" && !first {
						release := &protocol.BoundedRunnerAdmissionRelease{RequestID: "fresh-owner-release", HostID: r.cfg.HostID, RecoveryAuthorizationID: authority.RequestID,
							Successor: authority.Target, RunnerGeneration: input.Runtime.Generation, Journal: *authority.DataIdentity, ExpectedControlRevision: 0, Reason: "admit new work while original facts reconcile"}
						hello.Control = &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerNormal, BoundedRecoveryRelease: release}
					}
					result.Result, _ = json.Marshal(hello)
				case protocol.OpManagerNativeEvent:
					// The second pass is inside Run's pending-admission branch.
					// Keep this original unknown fact pending throughout the test.
					if events.Add(1) == 2 {
						waiting <- struct{}{}
					}
					result.Status = protocol.OpAccepted
				case protocol.OpRunnerClaim:
					claimed <- struct{}{}
					r.wakeNativeUsage()
					<-req.Context().Done()
					return
				case protocol.OpRunnerUsageUpload:
					uploads.Add(1)
					uploaded <- env
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &result})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			done := make(chan error, 1)
			go func() { done <- r.Run(ctx); close(done) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("runner did not cancel and join its startup goroutines")
				}
			}()
			if mode == "waiting_release" {
				select {
				case <-waiting:
					r.requestReconcile()
				case <-ctx.Done():
					t.Fatal("runner did not enter the existing bounded waiting branch")
				}
			}
			select {
			case <-claimed:
			case <-ctx.Done():
				t.Fatal("startup never admitted a new claim")
			}
			select {
			case envelope := <-uploaded:
				if !reflect.DeepEqual(envelope, original) {
					t.Fatal("startup changed the original usage envelope", envelope)
				}
			case <-ctx.Done():
				t.Fatal("claims were admitted without starting native usage recovery")
			}
			for {
				requests, err := r.cfg.Journal.Requests(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				pending := false
				for _, request := range requests {
					pending = pending || request.Envelope.RequestID == original.RequestID
				}
				if !pending {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("matching usage acknowledgement was not consumed")
				case <-time.After(time.Millisecond):
				}
			}
			assertStartupFactLoopCount(t, 1)
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal("runner did not return its cancellation after joining goroutines", err)
			}
			assertStartupFactLoopCount(t, 0)
			sources, err := r.cfg.Journal.UsageSources(t.Context())
			if err != nil || len(sources) != 1 {
				t.Fatal(sources, err)
			}
			retained := sources[0]
			if retained.Collection.Evidence != protocol.UsageUnknown || retained.Collection.State != protocol.UsageSettling {
				t.Fatal("startup changed unknown execution evidence", retained.Collection)
			}
			// Reading retries now persist their own lifecycle metadata. They
			// cannot reset the original source, cursor, coverage or upload.
			retained.Collection, retained.ExpectedRevision = protocol.UsageCollection{}, 0
			if !reflect.DeepEqual(retained, source) || uploads.Load() != 1 {
				t.Fatal("startup duplicated upload or changed unresolved source/cursor facts", sources, uploads.Load(), err)
			}
		})
	}
}

func assertStartupFactLoopCount(t *testing.T, want int) {
	t.Helper()
	buffer := make([]byte, 1<<20)
	stack := string(buffer[:runtime.Stack(buffer, true)])
	for _, name := range []string{"nativeUsageLoop", "finalMessageReceipts"} {
		if got := strings.Count(stack, "github.com/GhostFlying/tend/internal/runner.(*Runner)."+name+"("); got != want {
			t.Fatalf("startup %s goroutine count = %d, want %d", name, got, want)
		}
	}
}
