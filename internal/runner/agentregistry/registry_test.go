package agentregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func rid(n int) string {
	const base = "req_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	return base[:len(base)-1] + string(alphabet[n])
}
func configuration() agentconfig.Config {
	return agentconfig.Config{FormatVersion: 1,
		Runtimes: []agentconfig.RuntimeBinding{{ID: "runtime-a", Revision: 1, Kind: agentconfig.AppServerWorker, ExecutableRef: "opaque-local-install", DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true}},
		Agents:   []agentconfig.AgentDefinition{{LocalID: "agent-a", Revision: 1, DisplayName: "example", RuntimeID: "runtime-a", Categories: []string{"reviewer", "implementer"}, Purposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Model: "requested-model", Enabled: true}}}
}
func installation() []InstallationReference {
	return []InstallationReference{{RuntimeID: "runtime-a", ArtifactID: "artifact-a", ExpectedSHA256: "sha256:" + strings.Repeat("a", 64), AdapterID: "appserver-adapter", AdapterRevision: "declared-v1", LocalReference: "opaque-private-locator"}}
}
func candidate(t *testing.T, source string) *Candidate {
	t.Helper()
	c, err := Prepare("env-a", configuration(), agentconfig.SourceReference{Kind: agentconfig.LocalFile, Reference: source}, installation())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error=%v; want code %s", err, code)
	}
	if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "/") {
		t.Fatal("private value escaped error")
	}
}

type fixture struct {
	t     *testing.T
	path  string
	r     *Registry
	opts  Options
	ids   int
	times int
	fault string
	after bool
	steps int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, path: filepath.Join(t.TempDir(), "registry")}
	f.opts = Options{
		Now:           func() time.Time { f.times++; return time.Date(2026, 10, 29, 0, 0, f.times, 0, time.UTC) },
		NewSnapshotID: func() string { f.ids++; return fmt.Sprintf("snapshot-%04d", f.ids) },
		Before: func(step string) error {
			f.steps++
			if step == f.fault && !f.after {
				return errors.New("PRIVATE_FAULT")
			}
			return nil
		},
		After: func(step string) error {
			f.steps++
			if step == f.fault && f.after {
				return errors.New("PRIVATE_FAULT")
			}
			return nil
		},
	}
	r, err := Create(t.Context(), f.path, "env-a", f.opts)
	if err != nil {
		t.Fatal(err)
	}
	f.r = r
	t.Cleanup(func() { _ = f.r.Close() })
	return f
}
func (f *fixture) commit(n int, rev uint64, c *Candidate) Receipt {
	f.t.Helper()
	receipt, err := f.r.Commit(f.t.Context(), CommitRequest{rid(n), rev, c})
	if err != nil {
		f.t.Fatal(err)
	}
	return receipt
}
func (f *fixture) reopen() {
	f.t.Helper()
	_ = f.r.Close()
	r, err := Open(f.t.Context(), f.path, "env-a", f.opts)
	if err != nil {
		f.t.Fatal(err)
	}
	f.r = r
}
func observe(t *testing.T, r *Registry) Observation {
	t.Helper()
	v, err := r.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func diskBytes(t *testing.T, f *fixture) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.path, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPublishPrivateIdentityAndFormattedRestart(t *testing.T) {
	f := setup(t)
	cfg := configuration()
	pins := installation()
	c1, err := Prepare("env-a", cfg, agentconfig.SourceReference{Kind: agentconfig.LocalFile, Reference: "private-one"}, pins)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents[0].Model = "changed-after-prepare"
	pins[0].LocalReference = "changed-after-prepare"
	a := f.commit(1, 0, c1)
	old, err := f.r.Resolve(t.Context(), a.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	before := old.PrivatePayload()
	c2 := candidate(t, "private-two")
	b := f.commit(2, 1, c2)
	if c1.publicHash != c2.publicHash || a.Snapshot == b.Snapshot {
		t.Fatal("private identity was conflated with public hash")
	}
	// Owner formatting does not alter the authority's logical contents.
	for _, path := range []string{
		filepath.Join(f.path, "registry.json"),
		filepath.Join(f.path, "snapshots", b.Snapshot.ID+".json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var pretty bytes.Buffer
		if err = json.Indent(&pretty, raw, "", "  "); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(pretty.Bytes(), '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.reopen()
	old, err = f.r.Resolve(t.Context(), a.Snapshot)
	if err != nil || !bytes.Equal(old.PrivatePayload(), before) {
		t.Fatal("old reference did not survive", err)
	}
	copy := old.PrivatePayload()
	copy[0] = 'X'
	if !bytes.Equal(old.PrivatePayload(), before) {
		t.Fatal("returned snapshot aliases state")
	}
	var public protocol.AgentInventoryProjection
	if err = json.Unmarshal(old.PublicBytes(), &public); err != nil {
		t.Fatal(err)
	}
	if public.Agents[0].Availability.Available || public.Agents[0].Registration != "staged" {
		t.Fatal("storage enabled a runtime")
	}
}

func TestReplayPrecedesCASAndOtherPending(t *testing.T) {
	f := setup(t)
	c1 := candidate(t, "one")
	a := f.commit(1, 0, c1)
	c2 := candidate(t, "two")
	f.fault = "state.dir_sync"
	_, err := f.r.Commit(t.Context(), CommitRequest{rid(2), 1, c2})
	expectCode(t, err, "unknown")
	before := diskBytes(t, f)
	steps, ids, times := f.steps, f.ids, f.times
	again, err := f.r.Commit(t.Context(), CommitRequest{rid(1), 0, c1})
	if err != nil || again != a {
		t.Fatal("old success blocked by pending/CAS", err)
	}
	out, err := f.r.Reconcile(t.Context(), rid(1))
	if err != nil || out.Receipt == nil || *out.Receipt != a {
		t.Fatal("old receipt not reconciled", err)
	}
	if !bytes.Equal(before, diskBytes(t, f)) || steps != f.steps || ids != f.ids || times != f.times {
		t.Fatal("receipt replay created facts")
	}
	if obs := observe(t, f.r); !obs.Pending || obs.LastConfirmedSnapshot != a.Snapshot {
		t.Fatal("pending was incorrectly published")
	}
	for _, req := range []CommitRequest{{rid(1), 0, c2}, {rid(1), 1, c1}, {rid(2), 1, c1}} {
		_, err = f.r.Commit(t.Context(), req)
		expectCode(t, err, "request_conflict")
	}
	_, err = f.r.Commit(t.Context(), CommitRequest{rid(3), 1, c1})
	expectCode(t, err, "unknown")
	_, err = f.r.Resolve(t.Context(), SnapshotRef{"snapshot-0002"})
	expectCode(t, err, "snapshot_not_accepted")
	if err = os.WriteFile(filepath.Join(f.path, "snapshots", "orphan.json"), encode(c1.disk("orphan")), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.r.Resolve(t.Context(), SnapshotRef{"orphan"})
	expectCode(t, err, "snapshot_not_accepted")
	f.fault = ""
	out, err = f.r.Reconcile(t.Context(), rid(2))
	if err != nil || out.State != "accepted" || out.Receipt.Revision != 2 {
		t.Fatal("unknown did not resolve original receipt", err)
	}
	f.reopen()
	again, err = f.r.Commit(t.Context(), CommitRequest{rid(1), 0, c1})
	if err != nil || again != a {
		t.Fatal("restart lost old successful replay", err)
	}
	_, err = f.r.Resolve(t.Context(), SnapshotRef{"orphan"})
	expectCode(t, err, "snapshot_not_accepted")
}

func TestConcurrentCAS(t *testing.T) {
	f := setup(t)
	f.commit(1, 0, candidate(t, "original"))
	candidates := []*Candidate{candidate(t, "a"), candidate(t, "b")}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() { _, err := f.r.Commit(t.Context(), CommitRequest{rid(i + 2), 1, candidates[i]}); results <- err })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			expectCode(t, err, "revision_conflict")
		}
	}
	if success != 1 {
		t.Fatal("CAS did not choose one winner")
	}
	f.reopen()
	if observe(t, f.r).LastConfirmedRevision != 2 {
		t.Fatal("CAS state not recovered")
	}
}

func TestFault_ReachableCommitAndRecoveryWindows(t *testing.T) {
	cases := []struct {
		name, step string
		after      bool
		restart    bool
		unknown    bool
		next       bool
	}{
		{"snapshot-write-before-commit", "snapshot.write", false, true, false, false},
		{"control-sync-before-publication", "state.file_sync", false, true, false, false},
		{"rename-not-applied", "state.rename", false, true, true, false},
		{"rename-applied-response-error", "state.rename", true, false, true, true},
		{"published-directory-not-confirmed", "state.dir_sync", false, true, true, true},
		{"snapshot-published-control-not-started", "snapshot.dir_sync", false, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			a := f.commit(1, 0, candidate(t, "one"))
			c := candidate(t, "two")
			f.fault = tc.step
			f.after = tc.after
			_, err := f.r.Commit(t.Context(), CommitRequest{rid(2), 1, c})
			code := "storage_error"
			if tc.unknown {
				code = "unknown"
			}
			expectCode(t, err, code)
			if obs := observe(t, f.r); obs.LastConfirmedSnapshot != a.Snapshot || obs.Pending != tc.unknown {
				t.Fatal("failure changed confirmed state")
			}
			f.fault = ""
			if tc.restart {
				f.reopen()
			}
			out, err := f.r.Reconcile(t.Context(), rid(2))
			if err != nil {
				t.Fatal(err)
			}
			want := "not_recorded"
			if tc.unknown && !tc.restart {
				want = "not_committed"
			}
			if tc.next {
				want = "accepted"
			}
			if out.State != want {
				t.Fatalf("outcome %s want %s", out.State, want)
			}
			if tc.next {
				if out.Receipt == nil || out.Receipt.Revision != 2 {
					t.Fatal("lost original receipt")
				}
			} else {
				f.commit(2, 1, c)
			}
			f.reopen()
			if observe(t, f.r).LastConfirmedRevision != 2 {
				t.Fatal("restart lost pointer/receipt")
			}
			if _, err = f.r.Resolve(t.Context(), a.Snapshot); err != nil {
				t.Fatal("old reference lost", err)
			}
		})
	}
}

func TestHistoricalPayloadIsReadOnlyWhenUsed(t *testing.T) {
	f := setup(t)
	c1 := candidate(t, "old")
	a := f.commit(1, 0, c1)
	b := f.commit(2, 1, candidate(t, "current"))
	if err := os.Remove(filepath.Join(f.path, "snapshots", a.Snapshot.ID+".json")); err != nil {
		t.Fatal(err)
	}
	// Unused historical files do not prevent a healthy current from opening.
	f.reopen()
	if observe(t, f.r).LastConfirmedSnapshot != b.Snapshot {
		t.Fatal("Open fell back from current")
	}
	if _, err := f.r.Resolve(t.Context(), a.Snapshot); err == nil {
		t.Fatal("missing old payload was guessed")
	}
	// Reusing that old snapshot as current is an actual use, so it must fail.
	_, err := f.r.Commit(t.Context(), CommitRequest{rid(3), 2, c1})
	if err == nil || observe(t, f.r).LastConfirmedSnapshot != b.Snapshot {
		t.Fatal("missing reused snapshot became current")
	}
	out, err := f.r.Reconcile(t.Context(), rid(1))
	if err != nil || out.Receipt == nil || *out.Receipt != a {
		t.Fatal("historical receipt needs no historical payload", err)
	}
	f.commit(3, 2, candidate(t, "new-private"))
	f.reopen()
	if observe(t, f.r).LastConfirmedRevision != 3 {
		t.Fatal("unrelated old payload blocked new configuration")
	}
}

func TestCurrentStateCannotSilentlyReinitialize(t *testing.T) {
	for _, kind := range []string{"missing-current", "torn-authority"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			a := f.commit(1, 0, candidate(t, "one"))
			if kind == "missing-current" {
				if err := os.Remove(filepath.Join(f.path, "snapshots", a.Snapshot.ID+".json")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(f.path, "registry.json"), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_ = f.r.Close()
			before := diskBytes(t, f)
			r, err := Open(t.Context(), f.path, "env-a", f.opts)
			if err == nil || r != nil {
				t.Fatal("Open created an empty replacement")
			}
			if !bytes.Equal(before, diskBytes(t, f)) {
				t.Fatal("Open rewrote evidence")
			}
		})
	}
}

func TestFault_PersistenceStillUncertain(t *testing.T) {
	f := setup(t)
	f.commit(1, 0, candidate(t, "one"))
	f.fault = "state.dir_sync"
	_, err := f.r.Commit(t.Context(), CommitRequest{rid(2), 1, candidate(t, "two")})
	expectCode(t, err, "unknown")
	f.fault = "reconcile.root_sync"
	out, err := f.r.Reconcile(t.Context(), rid(2))
	if err == nil || out.State != "unknown" || !observe(t, f.r).Pending {
		t.Fatal("failed confirmation published next")
	}
	f.fault = ""
	out, err = f.r.Reconcile(t.Context(), rid(2))
	if err != nil || out.State != "accepted" {
		t.Fatal("original request could not settle", err)
	}
}

func TestPrepareTypedEmptyListsMatchExplicitArrays(t *testing.T) {
	for _, runtimeOnly := range []bool{false, true} {
		name := "empty"
		if runtimeOnly {
			name = "runtime-only"
		}
		t.Run(name, func(t *testing.T) {
			cfg := agentconfig.Config{FormatVersion: 1}
			var pins []InstallationReference
			if runtimeOnly {
				cfg.Runtimes = configuration().Runtimes
				pins = installation()
			}
			source := agentconfig.SourceReference{Kind: agentconfig.Inline}
			if _, err := agentconfig.Validate("env-a", cfg, source); err != nil {
				t.Fatal(err)
			}
			implicit, err := Prepare("env-a", cfg, source, pins)
			if err != nil {
				t.Fatalf("valid typed configuration was rejected: %v", err)
			}
			if cfg.Agents != nil || (!runtimeOnly && cfg.Runtimes != nil) {
				t.Fatal("Prepare changed the caller's lists")
			}
			explicit := cfg
			if explicit.Runtimes == nil {
				explicit.Runtimes = []agentconfig.RuntimeBinding{}
			}
			explicit.Agents = []agentconfig.AgentDefinition{}
			normalized, err := Prepare("env-a", explicit, source, pins)
			if err != nil {
				t.Fatal(err)
			}
			if implicit.privateHash != normalized.privateHash || implicit.publicHash != normalized.publicHash || !bytes.Equal(implicit.raw, normalized.raw) || !bytes.Equal(implicit.public, normalized.public) {
				t.Fatal("nil and explicit empty arrays have different identity")
			}
			f := setup(t)
			first := f.commit(1, 0, implicit)
			second := f.commit(2, 1, normalized)
			if first.Snapshot != second.Snapshot {
				t.Fatal("equivalent typed configuration created another snapshot")
			}
			f.reopen()
			restored, err := f.r.Resolve(t.Context(), first.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(restored.PrivatePayload(), implicit.raw) || !bytes.Equal(restored.PublicBytes(), implicit.public) {
				t.Fatal("typed configuration identity changed after restart")
			}
		})
	}
}
