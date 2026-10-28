package agentregistry

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

type CommitRequest struct {
	RequestID        string
	ExpectedRevision uint64
	Candidate        *Candidate
}
type pendingCommit struct {
	requestID        string
	expectedRevision uint64
	privateHash      string
	before           []byte
	next             []byte
}

// The runner lifecycle owns one writer. This package adds no process lock.
type Registry struct {
	gate    chan struct{}
	path    string
	opts    Options
	state   diskState
	pending *pendingCommit
	closed  bool
}

func (r *Registry) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.gate:
		return nil
	}
}
func (r *Registry) unlock() { r.gate <- struct{}{} }
func newRegistry(path, env string, opts Options) (*Registry, error) {
	if opts.Now == nil || opts.NewSnapshotID == nil {
		return nil, fail("required_options")
	}
	if _, err := agentconfig.Validate(env, agentconfig.Config{FormatVersion: 1}, agentconfig.SourceReference{Kind: agentconfig.Inline}); err != nil {
		return nil, fail("invalid_environment")
	}
	r := &Registry{path: path, opts: opts, gate: make(chan struct{}, 1), state: initialState(env)}
	r.unlock()
	return r, nil
}

func Create(ctx context.Context, path, env string, opts Options) (*Registry, error) {
	r, err := newRegistry(path, env, opts)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = os.Mkdir(path, 0o700); err != nil {
		return nil, storage(err)
	}
	if err = os.Mkdir(filepath.Join(path, "snapshots"), 0o700); err != nil {
		return nil, storage(err)
	}
	if err = r.confirmDirectories(ctx, "create"); err != nil {
		return nil, err
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, storage(err)
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return nil, storage(err)
	}
	unknown, err := r.persist(ctx, "registry.json", encode(r.state), "state")
	if err != nil {
		if unknown {
			return nil, fail("unknown")
		}
		return nil, storage(err)
	}
	return r, nil
}
func Open(ctx context.Context, path, env string, opts Options) (*Registry, error) {
	r, err := newRegistry(path, env, opts)
	if err != nil {
		return nil, err
	}
	st, err := r.load(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.confirmDirectories(ctx, "open"); err != nil {
		return nil, err
	}
	r.state = st
	return r, nil
}
func (r *Registry) confirmDirectories(ctx context.Context, label string) error {
	if err := r.syncDirectory(ctx, "snapshots", label+".snapshots_sync"); err != nil {
		return storage(err)
	}
	if err := r.syncDirectory(ctx, ".", label+".root_sync"); err != nil {
		return storage(err)
	}
	return nil
}
func entryFor(st diskState, ref SnapshotRef) (indexEntry, bool) {
	for _, entry := range st.Snapshots {
		if entry.Ref == ref {
			return entry, true
		}
	}
	return indexEntry{}, false
}
func (r *Registry) readSnapshot(entry indexEntry, env string) (*Candidate, error) {
	raw, err := r.read("snapshots/" + entry.Ref.ID + ".json")
	if err != nil {
		return nil, storage(err)
	}
	return decodeSnapshot(raw, entry, env)
}

// Check the authority's metadata and current payload. Historical payloads are
// read only when a caller actually resolves their accepted references.
func (r *Registry) load(ctx context.Context) (diskState, error) {
	var st diskState
	if err := ctx.Err(); err != nil {
		return st, err
	}
	raw, err := r.read("registry.json")
	if err != nil {
		return st, storage(err)
	}
	if strictDecode(raw, &st) != nil || st.Format != 1 || st.Environment != r.state.Environment ||
		uint64(len(st.Requests)) != st.Revision {
		return st, fail("invalid_state")
	}
	index := map[string]indexEntry{}
	for _, entry := range st.Snapshots {
		if !safeID(entry.Ref.ID) {
			return st, fail("invalid_state")
		}
		if _, ok := index[entry.Ref.ID]; ok {
			return st, fail("invalid_state")
		}
		index[entry.Ref.ID] = entry
	}
	seen := map[string]bool{}
	for i, record := range st.Requests {
		entry, ok := index[record.Receipt.Snapshot.ID]
		if !ok || seen[record.RequestID] || record.RequestID != record.Receipt.RequestID ||
			record.ExpectedRevision != uint64(i) || record.Receipt.Revision != uint64(i)+1 ||
			record.PrivateHash != entry.PrivateHash || record.Receipt.PublicHash != entry.PublicHash {
			return st, fail("invalid_state")
		}
		seen[record.RequestID] = true
	}
	if st.Revision == 0 {
		if st.Current.ID != "" {
			return st, fail("invalid_state")
		}
		return st, nil
	}
	if st.Current != st.Requests[len(st.Requests)-1].Receipt.Snapshot {
		return st, fail("invalid_state")
	}
	if _, err = r.readSnapshot(index[st.Current.ID], st.Environment); err != nil {
		return st, err
	}
	return st, nil
}

func (r *Registry) Commit(ctx context.Context, req CommitRequest) (Receipt, error) {
	if err := r.lock(ctx); err != nil {
		return Receipt{}, err
	}
	defer r.unlock()
	if r.closed {
		return Receipt{}, fail("closed")
	}
	c := req.Candidate
	if !protocol.ValidID(protocol.PrefixRequest, req.RequestID) || c == nil || len(c.raw) == 0 ||
		c.payload.Environment != r.state.Environment {
		return Receipt{}, fail("invalid_request")
	}
	for _, record := range r.state.Requests {
		if record.RequestID != req.RequestID {
			continue
		}
		if record.ExpectedRevision != req.ExpectedRevision || record.PrivateHash != c.privateHash {
			return Receipt{}, fail("request_conflict")
		}
		return record.Receipt, nil
	}
	if r.pending != nil {
		if r.pending.requestID == req.RequestID && (r.pending.expectedRevision != req.ExpectedRevision || r.pending.privateHash != c.privateHash) {
			return Receipt{}, fail("request_conflict")
		}
		return Receipt{}, fail("unknown")
	}
	if req.ExpectedRevision != r.state.Revision {
		return Receipt{}, fail("revision_conflict")
	}
	if r.state.Revision == math.MaxUint64 {
		return Receipt{}, fail("revision_exhausted")
	}
	at := r.opts.Now()
	if at.IsZero() {
		return Receipt{}, fail("invalid_clock")
	}
	if _, err := at.MarshalJSON(); err != nil {
		return Receipt{}, fail("invalid_clock")
	}
	ref := SnapshotRef{}
	for _, entry := range r.state.Snapshots {
		if entry.PrivateHash == c.privateHash {
			ref = entry.Ref
			break
		}
	}
	if ref.ID != "" {
		entry, _ := entryFor(r.state, ref)
		if _, err := r.readSnapshot(entry, r.state.Environment); err != nil {
			return Receipt{}, err
		}
	}
	before := encode(r.state)
	isNew := ref.ID == ""
	if isNew {
		ref.ID = r.opts.NewSnapshotID()
		if !safeID(ref.ID) {
			return Receipt{}, fail("invalid_snapshot_id")
		}
		if _, err := os.Stat(filepath.Join(r.path, "snapshots", ref.ID+".json")); err == nil {
			return Receipt{}, fail("snapshot_id_collision")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Receipt{}, storage(err)
		}
		unknown, err := r.persist(ctx, "snapshots/"+ref.ID+".json", encode(c.disk(ref.ID)), "snapshot")
		if err != nil {
			if unknown {
				r.pending = &pendingCommit{requestID: req.RequestID, expectedRevision: req.ExpectedRevision, privateHash: c.privateHash, before: before}
				return Receipt{}, fail("unknown")
			}
			return Receipt{}, storage(err)
		}
	}
	receipt := Receipt{RequestID: req.RequestID, Revision: r.state.Revision + 1, Snapshot: ref, PublicHash: c.publicHash, AcceptedAt: at}
	next := r.state
	next.Snapshots = append([]indexEntry{}, r.state.Snapshots...)
	next.Requests = append([]requestRecord{}, r.state.Requests...)
	next.Revision = receipt.Revision
	next.Current = ref
	if isNew {
		next.Snapshots = append(next.Snapshots, indexEntry{ref, c.privateHash, c.publicHash})
	}
	next.Requests = append(next.Requests, requestRecord{req.RequestID, req.ExpectedRevision, c.privateHash, receipt})
	raw := encode(next)
	unknown, err := r.persist(ctx, "registry.json", raw, "state")
	if err != nil {
		if unknown {
			r.pending = &pendingCommit{requestID: req.RequestID, expectedRevision: req.ExpectedRevision, privateHash: c.privateHash, before: before, next: raw}
			return Receipt{}, fail("unknown")
		}
		return Receipt{}, storage(err)
	}
	r.state = next
	return receipt, nil
}
func (r *Registry) Current(ctx context.Context) (Observation, error) {
	if err := r.lock(ctx); err != nil {
		return Observation{}, err
	}
	defer r.unlock()
	if r.closed {
		return Observation{}, fail("closed")
	}
	obs := Observation{LastConfirmedRevision: r.state.Revision, LastConfirmedSnapshot: r.state.Current}
	if entry, ok := entryFor(r.state, r.state.Current); ok {
		obs.PublicHash = entry.PublicHash
	}
	if r.pending != nil {
		obs.Pending = true
		obs.PendingRequestID = r.pending.requestID
	}
	return obs, nil
}
func (r *Registry) Reconcile(ctx context.Context, requestID string) (Outcome, error) {
	if err := r.lock(ctx); err != nil {
		return Outcome{}, err
	}
	defer r.unlock()
	if r.closed {
		return Outcome{}, fail("closed")
	}
	if !protocol.ValidID(protocol.PrefixRequest, requestID) {
		return Outcome{}, fail("invalid_request")
	}
	for _, record := range r.state.Requests {
		if record.RequestID == requestID {
			copy := record.Receipt
			return Outcome{State: "accepted", Receipt: &copy}, nil
		}
	}
	if r.pending == nil {
		return Outcome{State: "not_recorded"}, nil
	}
	p := r.pending
	if p.requestID != requestID {
		return Outcome{State: "unknown"}, fail("pending_other_request")
	}
	st, err := r.load(ctx)
	if err != nil {
		return Outcome{State: "unknown"}, err
	}
	actual := encode(st)
	isOld := bytes.Equal(actual, p.before)
	isNext := len(p.next) != 0 && bytes.Equal(actual, p.next)
	if !isOld && !isNext {
		return Outcome{State: "unknown"}, fail("unexpected_disk_state")
	}
	if err = r.confirmDirectories(ctx, "reconcile"); err != nil {
		return Outcome{State: "unknown"}, err
	}
	r.pending = nil
	if isOld {
		return Outcome{State: "not_committed"}, nil
	}
	r.state = st
	for _, record := range st.Requests {
		if record.RequestID == requestID {
			copy := record.Receipt
			return Outcome{State: "accepted", Receipt: &copy}, nil
		}
	}
	panic("internal next-state receipt invariant")
}
func (r *Registry) Resolve(ctx context.Context, ref SnapshotRef) (*Snapshot, error) {
	if err := r.lock(ctx); err != nil {
		return nil, err
	}
	defer r.unlock()
	if r.closed {
		return nil, fail("closed")
	}
	entry, ok := entryFor(r.state, ref)
	if !ok {
		return nil, fail("snapshot_not_accepted")
	}
	c, err := r.readSnapshot(entry, r.state.Environment)
	if err != nil {
		return nil, err
	}
	return &Snapshot{ref: ref, candidate: c}, nil
}
func (r *Registry) Close() error {
	<-r.gate
	defer r.unlock()
	r.closed = true
	return nil
}
