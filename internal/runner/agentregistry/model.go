// Package agentregistry stores validated local configuration snapshots.
// It does not load factories or establish runtime availability.
package agentregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/agentconfig"
)

type Error struct {
	Code  string
	cause error
}

func (e *Error) Error() string { return "agent registry: " + e.Code }
func (e *Error) Unwrap() error { return e.cause }
func fail(code string) error   { return &Error{Code: code} }
func storage(err error) error  { return &Error{Code: "storage_error", cause: err} }

type InstallationReference struct {
	RuntimeID       string
	ArtifactID      string
	ExpectedSHA256  string
	AdapterID       string
	AdapterRevision string
	LocalReference  string
}

type privatePayload struct {
	Format        uint64
	Environment   string
	Config        agentconfig.Config
	Source        agentconfig.SourceReference
	Installations []InstallationReference
}

// Candidate can only be constructed by Prepare; all fields are detached.
type Candidate struct {
	payload     privatePayload
	raw         []byte
	privateHash string
	public      []byte
	publicHash  string
}

func Prepare(env string, cfg agentconfig.Config, source agentconfig.SourceReference, installs []InstallationReference) (*Candidate, error) {
	// Preserve private identity rather than accepting JSON's UTF-8 replacement.
	if !utf8.ValidString(source.Reference) {
		return nil, fail("invalid_private_reference")
	}
	for _, binding := range cfg.Runtimes {
		if !utf8.ValidString(binding.ExecutableRef) {
			return nil, fail("invalid_private_reference")
		}
	}
	// Validate typed callers, then detach every slice without adding P2a getters.
	if _, err := agentconfig.Validate(env, cfg, source); err != nil {
		return nil, err
	}
	// Typed empty lists are valid; detach them as arrays for the strict decoder.
	if cfg.Runtimes == nil {
		cfg.Runtimes = []agentconfig.RuntimeBinding{}
	}
	if cfg.Agents == nil {
		cfg.Agents = []agentconfig.AgentDefinition{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fail("invalid_candidate")
	}
	cfg, err = agentconfig.Decode(raw)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(cfg.Runtimes, func(a, b agentconfig.RuntimeBinding) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(cfg.Agents, func(a, b agentconfig.AgentDefinition) int { return strings.Compare(a.LocalID, b.LocalID) })
	for i := range cfg.Runtimes {
		slices.Sort(cfg.Runtimes[i].DeclaredPurposes)
	}
	for i := range cfg.Agents {
		slices.Sort(cfg.Agents[i].Categories)
		slices.Sort(cfg.Agents[i].Purposes)
	}
	installs = slices.Clone(installs)
	if installs == nil {
		installs = []InstallationReference{}
	}
	slices.SortFunc(installs, func(a, b InstallationReference) int { return strings.Compare(a.RuntimeID, b.RuntimeID) })
	if len(installs) != len(cfg.Runtimes) {
		return nil, fail("installation_mismatch")
	}
	artifacts := map[string]string{}
	for i, pin := range installs {
		if pin.RuntimeID != cfg.Runtimes[i].ID || !safeID(pin.ArtifactID) ||
			!nonempty(pin.AdapterID) || !nonempty(pin.AdapterRevision) || !nonempty(pin.LocalReference) ||
			!validDigest(pin.ExpectedSHA256) {
			return nil, fail("invalid_installation_reference")
		}
		key := artifactIdentity(pin)
		if old, ok := artifacts[pin.ArtifactID]; ok && old != key {
			return nil, fail("artifact_conflict")
		}
		artifacts[pin.ArtifactID] = key
	}
	defs, err := agentconfig.Validate(env, cfg, source)
	if err != nil {
		return nil, err
	}
	pub, publicHash, err := defs.CanonicalPublic()
	if err != nil {
		return nil, err
	}
	p := privatePayload{Format: 1, Environment: env, Config: cfg, Source: source, Installations: installs}
	raw, err = json.Marshal(p)
	if err != nil {
		return nil, fail("invalid_candidate")
	}
	return &Candidate{payload: p, raw: raw, privateHash: digest("tend-agent-private-snapshot-draft-v1\n", raw), public: pub, publicHash: publicHash}, nil
}

func digest(domain string, raw []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(domain))
	_, _ = h.Write(raw)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
func validDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s[7:])
	return err == nil
}
func safeID(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for i, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i == 0 || c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}
func nonempty(s string) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "\x00\r\n")
}
func artifactIdentity(p InstallationReference) string {
	b, _ := json.Marshal([]string{p.ExpectedSHA256, p.AdapterID, p.AdapterRevision})
	return string(b)
}

type SnapshotRef struct{ ID string }
type Receipt struct {
	RequestID  string
	Revision   uint64
	Snapshot   SnapshotRef
	PublicHash string
	AcceptedAt time.Time
}
type Observation struct {
	LastConfirmedRevision uint64
	LastConfirmedSnapshot SnapshotRef
	PublicHash            string
	PendingRequestID      string
	Pending               bool
}
type Outcome struct {
	State   string
	Receipt *Receipt
}
type Snapshot struct {
	ref       SnapshotRef
	candidate *Candidate
}

func (s *Snapshot) Ref() SnapshotRef { return s.ref }

// PrivatePayload is local data, not a server inventory serialization.
func (s *Snapshot) PrivatePayload() []byte { return slices.Clone(s.candidate.raw) }
func (s *Snapshot) PublicBytes() []byte    { return slices.Clone(s.candidate.public) }

type snapshotDisk struct {
	Format      uint64
	ID          string
	PrivateHash string
	PublicHash  string
	Payload     []byte
}
type indexEntry struct {
	Ref         SnapshotRef
	PrivateHash string
	PublicHash  string
}
type requestRecord struct {
	RequestID        string
	ExpectedRevision uint64
	PrivateHash      string
	Receipt          Receipt
}
type diskState struct {
	Format      uint64
	Environment string
	Revision    uint64
	Current     SnapshotRef
	Snapshots   []indexEntry
	Requests    []requestRecord
}

func initialState(env string) diskState {
	return diskState{Format: 1, Environment: env, Snapshots: []indexEntry{}, Requests: []requestRecord{}}
}
func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("internal prototype encoding invariant: %T", v))
	}
	return b
}
func (c *Candidate) disk(id string) snapshotDisk {
	return snapshotDisk{1, id, c.privateHash, c.publicHash, c.raw}
}
func decodeSnapshot(raw []byte, entry indexEntry, env string) (*Candidate, error) {
	var s snapshotDisk
	if err := strictDecode(raw, &s); err != nil || s.Format != 1 || s.ID != entry.Ref.ID || !safeID(s.ID) ||
		s.PrivateHash != entry.PrivateHash || s.PublicHash != entry.PublicHash {
		return nil, fail("invalid_snapshot")
	}
	var p privatePayload
	if err := strictDecode(s.Payload, &p); err != nil || p.Format != 1 || p.Environment != env {
		return nil, fail("invalid_snapshot")
	}
	c, err := Prepare(p.Environment, p.Config, p.Source, p.Installations)
	if err != nil || c.privateHash != s.PrivateHash || c.publicHash != s.PublicHash {
		return nil, fail("invalid_snapshot")
	}
	return c, nil
}

// Configuration returns detached private values for the local execution adapter.
func (s *Snapshot) Configuration() (agentconfig.Config, []InstallationReference, error) {
	var p privatePayload
	if err := json.Unmarshal(s.candidate.raw, &p); err != nil {
		return agentconfig.Config{}, nil, err
	}
	return p.Config, p.Installations, nil
}
