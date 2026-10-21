package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SessionKey is the thread key of an agent session's baseline and packets.
func SessionKey(agent protocol.AgentKind, sessionID string) string {
	return string(agent) + ":" + sessionID
}

// ComposeRequest asks for the context of an agent session.
type ComposeRequest struct {
	// Self is the session's principal; a delta leaves out the events its
	// own requests recorded.
	Self protocol.Principal
	// Full asks for a full snapshot; otherwise the context is a delta of
	// the changes after Watermark, or nothing. A full snapshot also tells
	// the results after Watermark, when there is one: the snapshot lists
	// only unfinished tasks, and the session has not heard of those
	// results.
	Full      bool
	Epoch     int64
	Watermark int64
	PacketID  string
}

// Composed is a session's context and the ledger position it reflects.
type Composed struct {
	// Text is the packet; empty when there is nothing to tell.
	Text string
	// ToSeq is the last event the context reflects.
	ToSeq int64
}

// ComposeFunc decides a session's context from the ledger as q sees it.
type ComposeFunc func(ctx context.Context, q store.Querier, r ComposeRequest) (Composed, error)

// AgentHook handles agent.hook: an agent the owner works in asks what to
// add to its context (D47). Only a session that has called a tend tool
// gets any (D54); tend leaves every other session alone, such as an
// agent's background sessions, and records nothing about it. A session
// start of any kind, or a session tend has no valid baseline for, gets a
// full snapshot; otherwise the session gets the changes since its
// watermark that it should hear about, or nothing. The watermark moves on as the answer is recorded: tend cannot
// tell whether the agent passed the context on, and a write based on what
// it missed is caught by its revision check.
type AgentHook struct {
	Compose ComposeFunc
}

type hookPayload struct {
	Event  protocol.HookEvent `json:"event"`
	Source string             `json:"source,omitempty"`
	// Remotes are the git remotes of the repository the session works in,
	// which bind it to a project (D59).
	Remotes []string `json:"remotes,omitempty"`
}

// Operation implements Command.
func (AgentHook) Operation() string { return "agent.hook" }

// Authorize implements Command.
func (AgentHook) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if !x.Principal.Addon() || x.Principal.Thread == "" {
		return protocol.Errorf(protocol.CodeForbidden, "only an agent session may ask for its context")
	}
	return nil
}

// Apply implements Command.
func (c AgentHook) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p hookPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if !p.Event.Valid() {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown hook event %q", p.Event)
	}
	self := x.Principal
	key := SessionKey(self.Agent, self.Thread)
	if x.Env.Target.ID != key {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "the target must be the calling session %s", key)
	}
	bound, err := store.AgentSessionBound(ctx, tx.Q(), string(self.Agent), self.Thread)
	if err != nil {
		return Outcome{}, err
	}
	if !bound {
		return Outcome{Result: protocol.HookResponse{}, NoEffect: true}, nil
	}
	now := x.Now.UnixMilli()
	if err := tx.UpsertAgentSession(ctx, string(self.Agent), self.Thread, self.Subject, now); err != nil {
		return Outcome{}, err
	}
	// A session that moved to another project starts over with a full
	// snapshot of that project.
	moved, err := bindSessionProject(ctx, tx, self, p.Remotes, false)
	if err != nil {
		return Outcome{}, err
	}
	base, err := store.GetBaseline(ctx, tx.Q(), key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	full := p.Event == protocol.HookSessionStart || moved || err != nil || !base.Valid
	if err != nil {
		base = store.Baseline{ThreadKey: key}
	}
	if full {
		// A packet the session saw before no longer counts: the summary of
		// a compaction may still carry it, so the snapshot says it replaces
		// everything earlier.
		base.Epoch++
	}
	packetID := x.IDs.New(protocol.PrefixPacket)
	out, err := c.Compose(ctx, tx.Q(), ComposeRequest{Self: self, Full: full, Epoch: base.Epoch, Watermark: base.Watermark, PacketID: packetID})
	if err != nil {
		return Outcome{}, err
	}
	if out.Text != "" {
		kind, from := "delta", base.Watermark+1
		if full {
			kind, from = "full", 1
		}
		if err := tx.InsertPacket(ctx, store.Packet{
			PacketID: packetID, ThreadKey: key, Epoch: base.Epoch, Kind: kind, FromSeq: from, ToSeq: out.ToSeq, Body: out.Text,
		}, now); err != nil {
			return Outcome{}, err
		}
		if err := tx.SetPacketStatus(ctx, packetID, store.PacketBuilt, store.PacketInjected, now); err != nil {
			return Outcome{}, err
		}
		base.LastPacketID = packetID
	}
	base.Watermark, base.Valid = out.ToSeq, true
	if err := tx.PutBaseline(ctx, base, now); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: protocol.HookResponse{Context: out.Text}}, nil
}

// BindAgentSession records that an agent session called a tend tool, so
// its hooks get context from now on (D54), and binds it to the project of
// the repository at remotes (D59). Binding is idempotent bookkeeping, like
// a heartbeat: it records no request and no event.
//
// A newly bound session starts with a watermark at the end of the ledger
// but no valid baseline: its next hook gets a full snapshot, and the
// results since binding with it, so a task it created and that finished
// before that hook is not lost.
func (a *App) BindAgentSession(ctx context.Context, p protocol.Principal, remotes []string) error {
	if !p.Addon() || p.Thread == "" {
		return nil
	}
	now := a.now().UnixMilli()
	key := SessionKey(p.Agent, p.Thread)
	return a.store.Write(ctx, func(tx *store.Tx) error {
		bound, err := store.AgentSessionBound(ctx, tx.Q(), string(p.Agent), p.Thread)
		if err != nil {
			return err
		}
		if !bound {
			if err := bindNewSession(ctx, tx, p, now); err != nil {
				return err
			}
		}
		// The bridge's remotes are those of where the agent started it.
		moved, err := bindSessionProject(ctx, tx, p, remotes, true)
		if err != nil || !moved {
			return err
		}
		return invalidateBaseline(ctx, tx, key, now)
	})
}

func bindNewSession(ctx context.Context, tx *store.Tx, p protocol.Principal, now int64) error {
	if err := tx.BindAgentSession(ctx, string(p.Agent), p.Thread, p.Subject, now); err != nil {
		return err
	}
	key := SessionKey(p.Agent, p.Thread)
	if _, err := store.GetBaseline(ctx, tx.Q(), key); !errors.Is(err, store.ErrNotFound) {
		return err
	}
	latest, err := store.LatestEventSeq(ctx, tx.Q())
	if err != nil {
		return err
	}
	return tx.PutBaseline(ctx, store.Baseline{ThreadKey: key, Watermark: latest}, now)
}

// SelectProject handles agent.select_project: an agent session chooses the
// project it works in, or every project, in place of the one tend bound
// from its repository (D59). The choice lasts as long as the session, and
// its next hook answers with a full snapshot. Like binding, the choice is
// bookkeeping and records no event.
type SelectProject struct{}

type selectProjectPayload struct {
	// Project is empty for every project.
	Project string `json:"project"`
}

// Operation implements Command.
func (SelectProject) Operation() string { return "agent.select_project" }

// Authorize implements Command.
func (SelectProject) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if !x.Principal.Addon() || x.Principal.Thread == "" {
		return protocol.Errorf(protocol.CodeForbidden, "only an agent session may choose its project")
	}
	return nil
}

// Apply implements Command.
func (SelectProject) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p selectProjectPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	self := x.Principal
	key := SessionKey(self.Agent, self.Thread)
	if x.Env.Target.ID != key {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "the target must be the calling session %s", key)
	}
	if p.Project != "" {
		if _, err := resolveProject(ctx, tx.Q(), p.Project, protocol.Principal{}); err != nil {
			return Outcome{}, err
		}
	}
	now := x.Now.UnixMilli()
	bound, err := store.AgentSessionBound(ctx, tx.Q(), string(self.Agent), self.Thread)
	if err != nil {
		return Outcome{}, err
	}
	if !bound {
		if err := bindNewSession(ctx, tx, self, now); err != nil {
			return Outcome{}, err
		}
	}
	if err := tx.SetSessionProject(ctx, string(self.Agent), self.Thread, store.SessionProject{Project: p.Project, Source: store.ProjectExplicit}); err != nil {
		return Outcome{}, err
	}
	if err := invalidateBaseline(ctx, tx, key, now); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"project": p.Project}}, nil
}
