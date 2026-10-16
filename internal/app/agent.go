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
	// the changes after Watermark, or nothing.
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
// add to its context (D47). A session start of any kind, or a session tend
// has no valid baseline for, gets a full snapshot; otherwise the session
// gets the changes since its watermark that it should hear about, or
// nothing. The watermark moves on as the answer is recorded: tend cannot
// tell whether the agent passed the context on, and a write based on what
// it missed is caught by its revision check.
type AgentHook struct {
	Compose ComposeFunc
}

type hookPayload struct {
	Event  protocol.HookEvent `json:"event"`
	Source string             `json:"source,omitempty"`
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
	now := x.Now.UnixMilli()
	if err := tx.UpsertAgentSession(ctx, string(self.Agent), self.Thread, self.Subject, now); err != nil {
		return Outcome{}, err
	}
	base, err := store.GetBaseline(ctx, tx.Q(), key)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	full := p.Event == protocol.HookSessionStart || err != nil || !base.Valid
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
