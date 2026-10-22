package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type deferredWorkspaceCleaner struct {
	mu       sync.Mutex
	failures map[string]bool
	attempts []string
}

func (*deferredWorkspaceCleaner) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	return Prepared{}, nil
}
func (*deferredWorkspaceCleaner) Collect(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error) {
	return protocol.ArtifactPayload{}, nil
}
func (*deferredWorkspaceCleaner) Finish(context.Context, protocol.Assignment) error { return nil }
func (c *deferredWorkspaceCleaner) Cleanup(_ context.Context, p protocol.WorkspaceCleanup) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts = append(c.attempts, p.WorkspaceID)
	if c.failures[p.WorkspaceID] {
		return errors.New("workspace still has live terminals")
	}
	return nil
}

type workspaceMaintenanceFixture struct {
	r               *Runner
	j               *journal.Journal
	cleaner         *deferredWorkspaceCleaner
	mu              sync.Mutex
	resources       []protocol.WorkspaceCleanup
	reservations    map[string]bool
	released        map[string]bool
	releaseRequests map[string]map[string]bool
	doneRequests    map[string]map[string]bool
	failRelease     map[string]bool
	failDone        map[string]bool
	failReserve     bool
}

func newWorkspaceMaintenanceFixture(t *testing.T, resources ...string) *workspaceMaintenanceFixture {
	t.Helper()
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	f := &workspaceMaintenanceFixture{j: j, cleaner: &deferredWorkspaceCleaner{failures: map[string]bool{}}, reservations: map[string]bool{}, released: map[string]bool{}, releaseRequests: map[string]map[string]bool{}, doneRequests: map[string]map[string]bool{}, failRelease: map[string]bool{}, failDone: map[string]bool{}}
	for _, id := range resources {
		f.resources = append(f.resources, protocol.WorkspaceCleanup{WorkspaceID: id, CleanupID: "cleanup-" + id, Kind: "directory"})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var result any
		switch env.Operation {
		case protocol.OpWorkspaceReconcile:
			result = protocol.WorkspaceReconcileResult{}
		case protocol.OpWorkspaceCleanup:
			f.reservations[env.RequestID] = true
			if f.failReserve {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeInternal, "reservation response unavailable")})
				return
			}
			result = protocol.WorkspaceCleanupResult{Workspaces: f.resources}
		case protocol.OpWorkspaceRelease:
			var p protocol.WorkspaceRelease
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.releaseRequests[p.WorkspaceID] == nil {
				f.releaseRequests[p.WorkspaceID] = map[string]bool{}
			}
			f.releaseRequests[p.WorkspaceID][env.RequestID] = true
			if f.failRelease[p.WorkspaceID] {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeInternal, "release service unavailable")})
				return
			}
			f.released[p.WorkspaceID] = true
			result = protocol.WorkspaceReleaseResult{Released: true}
		case protocol.OpWorkspaceCleanupDone:
			var p protocol.WorkspaceCleanupDone
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.doneRequests[p.WorkspaceID] == nil {
				f.doneRequests[p.WorkspaceID] = map[string]bool{}
			}
			f.doneRequests[p.WorkspaceID][env.RequestID] = true
			if f.failDone[p.WorkspaceID] {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeInternal, "cleanup confirmation unavailable")})
				return
			}
			f.resources = slices.DeleteFunc(f.resources, func(resource protocol.WorkspaceCleanup) bool { return resource.WorkspaceID == p.WorkspaceID })
			result = map[string]bool{"removed": true}
		default:
			t.Errorf("unexpected maintenance operation %s", env.Operation)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b, err := json.Marshal(result)
		if err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: b}})
	}))
	t.Cleanup(server.Close)
	f.r = New(Config{HostID: "host", HostToken: "test-token", Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), Workspaces: f.cleaner, Log: slog.New(slog.DiscardHandler)})
	return f
}
func (f *workspaceMaintenanceFixture) queueRelease(t *testing.T, id string) {
	t.Helper()
	if err := f.j.QueueWorkspaceRelease(t.Context(), protocol.WorkspaceRelease{WorkspaceID: id, TaskID: "task-" + id}); err != nil {
		t.Fatal(err)
	}
}
func (f *workspaceMaintenanceFixture) queueCleanup(t *testing.T) {
	t.Helper()
	env, err := f.r.newEnvelope(protocol.OpWorkspaceCleanup, protocol.Target{Type: "host", ID: "host"}, protocol.WorkspaceCleanupRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.j.PutRequest(t.Context(), journal.Request{Envelope: *env}); err != nil {
		t.Fatal(err)
	}
}

func TestDeferredCleanupDoesNotStarveUnrelatedWorkspaceRelease(t *testing.T) {
	f := newWorkspaceMaintenanceFixture(t, "old")
	f.cleaner.failures["old"] = true
	f.queueCleanup(t)
	f.queueRelease(t, "new")
	for range 3 {
		if err := f.r.maintainWorkspaces(t.Context()); err == nil || !strings.Contains(err.Error(), "live terminals") {
			t.Fatalf("missing deferred cleanup error: %v", err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.released["new"] {
		t.Fatal("unrelated completed task stayed occupied behind old cleanup")
	}
	if len(f.reservations) != 3 {
		t.Fatalf("confirmed reservations did not permit fresh visits: %d", len(f.reservations))
	}
	pending, err := f.j.Requests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("confirmed authorization left redundant runner intent: %+v", pending)
	}
	releases, err := f.j.WorkspaceReleases(t.Context())
	if err != nil || len(releases) != 0 {
		t.Fatalf("completed release left queued: %+v %v", releases, err)
	}
}

func TestCleanupBatchContinuesAfterOneResourceIsDeferred(t *testing.T) {
	f := newWorkspaceMaintenanceFixture(t, "blocked", "available")
	f.cleaner.failures["blocked"] = true
	if err := f.r.maintainWorkspaces(t.Context()); err == nil {
		t.Fatal("missing deferred cleanup")
	}
	f.cleaner.mu.Lock()
	attempts := slices.Clone(f.cleaner.attempts)
	f.cleaner.mu.Unlock()
	if !slices.Equal(attempts, []string{"blocked", "available"}) {
		t.Fatalf("later independent resource not cleaned: %v", attempts)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.doneRequests["available"]) != 1 || len(f.doneRequests["blocked"]) != 0 {
		t.Fatalf("wrong cleanup confirmations: %+v", f.doneRequests)
	}
	pending, err := f.j.Requests(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("confirmed batch should live on server: %+v %v", pending, err)
	}
}

func TestCleanupConfirmationRetriesKeepOneDurableIdentity(t *testing.T) {
	f := newWorkspaceMaintenanceFixture(t, "retry", "other")
	f.failDone["retry"] = true
	for range 4 {
		if err := f.r.maintainWorkspaces(t.Context()); err == nil {
			t.Fatal("missing confirmation failure")
		}
	}
	f.mu.Lock()
	if len(f.reservations) != 4 || len(f.doneRequests["retry"]) != 1 || len(f.doneRequests["other"]) == 0 {
		t.Fatalf("failed confirmation duplicated identities or blocked later resources: reservations=%d confirmations=%+v", len(f.reservations), f.doneRequests)
	}
	f.failDone["retry"] = false
	f.mu.Unlock()
	f.cleaner.mu.Lock()
	retryCleanups := 0
	for _, id := range f.cleaner.attempts {
		if id == "retry" {
			retryCleanups++
		}
	}
	f.cleaner.mu.Unlock()
	if retryCleanups != 1 {
		t.Fatalf("completed local removal was repeated instead of resending confirmation: %d", retryCleanups)
	}
	if err := f.r.maintainWorkspaces(t.Context()); err != nil {
		t.Fatalf("confirmation recovery: %v", err)
	}
	pending, err := f.j.Requests(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("successful recovery left intents: %+v %v", pending, err)
	}
}

func TestFailedReleaseDoesNotStarveOthersOrDuplicateItsIntent(t *testing.T) {
	f := newWorkspaceMaintenanceFixture(t)
	f.failRelease["blocked"] = true
	f.queueRelease(t, "blocked")
	f.queueRelease(t, "available")
	for range 3 {
		if err := f.r.maintainWorkspaces(t.Context()); err == nil {
			t.Fatal("missing release failure")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.released["available"] || len(f.releaseRequests["blocked"]) != 1 {
		t.Fatalf("release starvation or duplicate retry identity: released=%v requests=%v", f.released, f.releaseRequests)
	}
	pending, err := f.j.Requests(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Envelope.Operation != protocol.OpWorkspaceRelease {
		t.Fatalf("failed release journal growth: %+v %v", pending, err)
	}
}

func TestUnknownCleanupReservationRetainsOneRequestIdentity(t *testing.T) {
	f := newWorkspaceMaintenanceFixture(t, "resource")
	f.failReserve = true
	f.queueRelease(t, "unrelated")
	for range 3 {
		if err := f.r.maintainWorkspaces(t.Context()); err == nil {
			t.Fatal("missing unknown reservation error")
		}
	}
	f.mu.Lock()
	if len(f.reservations) != 1 || !f.released["unrelated"] {
		t.Fatalf("unknown authorization duplicated or blocked release: %v %v", f.reservations, f.released)
	}
	f.failReserve = false
	f.mu.Unlock()
	pending, err := f.j.Requests(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Envelope.Operation != protocol.OpWorkspaceCleanup {
		t.Fatalf("unknown authorization not retained: %v %v", pending, err)
	}
	if err := f.r.maintainWorkspaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err = f.j.Requests(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("confirmed recovery left requests: %v %v", pending, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reservations) != 1 {
		t.Fatalf("recovery changed authorization identity: %v", f.reservations)
	}
}
