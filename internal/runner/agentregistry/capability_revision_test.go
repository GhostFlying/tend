package agentregistry

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

func capabilityCandidate(t *testing.T, cfg agentconfig.Config) *Candidate {
	t.Helper()
	c, err := Prepare("env-a", cfg, agentconfig.SourceReference{Kind: agentconfig.Inline}, installation())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCapabilityDeclarationRevisionCannotBeReused(t *testing.T) {
	one := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	two := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 2}}
	for _, tc := range []struct {
		name          string
		before, after []protocol.CapabilityReference
		stripVersion  bool
	}{{"add", nil, one, false}, {"remove", one, nil, false}, {"definition revision", one, two, false}, {"remove extension", one, nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			cfg := configuration()
			cfg.CapabilityVersion = 1
			cfg.Agents[0].Capabilities = tc.before
			original := capabilityCandidate(t, cfg)
			first := f.commit(1, 0, original)
			f.reopen()
			cfg.Agents[0].Capabilities = tc.after
			if tc.stripVersion {
				cfg.CapabilityVersion = 0
			}
			changed := capabilityCandidate(t, cfg)
			_, err := f.r.Commit(t.Context(), CommitRequest{rid(2), 1, changed})
			if err == nil {
				t.Fatal("changed capability declaration accepted under the same Agent revision")
			}
			expectCode(t, err, "capability_revision_conflict")
			obs := observe(t, f.r)
			files, err := os.ReadDir(filepath.Join(f.path, "snapshots"))
			if err != nil || obs.LastConfirmedRevision != 1 || obs.LastConfirmedSnapshot != first.Snapshot || len(files) != 1 {
				t.Fatal("rejected declaration changed accepted state", obs, files, err)
			}
			cfg.Agents[0].Revision++
			bumped := capabilityCandidate(t, cfg)
			second := f.commit(3, 1, bumped)
			if replay := f.commit(1, 0, original); replay != first {
				t.Fatal("original replay changed", replay, first)
			}
			if replay := f.commit(3, 1, bumped); replay != second {
				t.Fatal("version bump replay changed", replay, second)
			}
			old, err := f.r.Resolve(t.Context(), first.Snapshot)
			if err != nil || !bytes.Equal(old.PublicBytes(), original.public) {
				t.Fatal("historical snapshot changed", err)
			}
		})
	}
}

func TestCapabilityDeclarationRevisionChecksAllAcceptedHistory(t *testing.T) {
	f := setup(t)
	cfg := configuration()
	cfg.CapabilityVersion = 1
	original := capabilityCandidate(t, cfg)
	first := f.commit(1, 0, original)
	cfg.Agents = nil
	f.commit(2, 1, capabilityCandidate(t, cfg))
	f.reopen()
	cfg = configuration()
	cfg.CapabilityVersion = 1
	cfg.Agents[0].Capabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	_, err := f.r.Commit(t.Context(), CommitRequest{rid(3), 2, capabilityCandidate(t, cfg)})
	expectCode(t, err, "capability_revision_conflict")
	cfg.Agents[0].Revision = 2
	f.commit(4, 2, capabilityCandidate(t, cfg))
	// The old revision is still reserved even after a newer revision is current.
	cfg.Agents[0].Revision = 1
	_, err = f.r.Commit(t.Context(), CommitRequest{rid(5), 3, capabilityCandidate(t, cfg)})
	expectCode(t, err, "capability_revision_conflict")
	// Selecting the original immutable declaration remains a valid rollback.
	if restored := f.commit(6, 3, original); restored.Snapshot != first.Snapshot {
		t.Fatal("historical declaration did not retain its snapshot", restored, first)
	}
	if replay := f.commit(1, 0, original); replay != first {
		t.Fatal("original request replay lost", replay, first)
	}
}

func TestCapabilityDeclarationRevisionConcurrentCAS(t *testing.T) {
	f := setup(t)
	cfg := configuration()
	cfg.CapabilityVersion = 1
	f.commit(1, 0, capabilityCandidate(t, cfg))
	candidates := make([]*Candidate, 2)
	for i := range candidates {
		cfg.Agents[0].Revision = 2
		cfg.Agents[0].Capabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: int64(i + 1)}}
		candidates[i] = capabilityCandidate(t, cfg)
	}
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Go(func() {
			_, results[i] = f.r.Commit(t.Context(), CommitRequest{rid(i + 2), 1, candidates[i]})
		})
	}
	wg.Wait()
	loser := -1
	for i, err := range results {
		if err != nil {
			expectCode(t, err, "revision_conflict")
			if loser != -1 {
				t.Fatal("both concurrent registrations lost")
			}
			loser = i
		}
	}
	if loser == -1 {
		t.Fatal("CAS accepted both declarations")
	}
	f.reopen()
	_, err := f.r.Commit(t.Context(), CommitRequest{rid(4), 2, candidates[loser]})
	expectCode(t, err, "capability_revision_conflict")
}

func TestCapabilityDeclarationRevisionUnknownKeepsOriginalRequest(t *testing.T) {
	f := setup(t)
	cfg := configuration()
	cfg.CapabilityVersion = 1
	original := capabilityCandidate(t, cfg)
	first := f.commit(1, 0, original)
	cfg.Agents[0].Revision = 2
	cfg.Agents[0].Capabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	changed := capabilityCandidate(t, cfg)
	f.fault = "state.dir_sync"
	_, err := f.r.Commit(t.Context(), CommitRequest{rid(2), 1, changed})
	expectCode(t, err, "unknown")
	if replay := f.commit(1, 0, original); replay != first {
		t.Fatal("pending declaration lost original replay")
	}
	cfg.Agents[0].Capabilities = nil
	conflict := capabilityCandidate(t, cfg)
	_, err = f.r.Commit(t.Context(), CommitRequest{rid(2), 1, conflict})
	expectCode(t, err, "request_conflict")
	_, err = f.r.Commit(t.Context(), CommitRequest{rid(3), 1, conflict})
	expectCode(t, err, "unknown")
	f.fault = ""
	out, err := f.r.Reconcile(t.Context(), rid(2))
	if err != nil || out.State != "accepted" || out.Receipt == nil || out.Receipt.RequestID != rid(2) {
		t.Fatal("unknown did not resolve original declaration request", out, err)
	}
	f.reopen()
	_, err = f.r.Commit(t.Context(), CommitRequest{rid(3), 2, conflict})
	expectCode(t, err, "capability_revision_conflict")
}
