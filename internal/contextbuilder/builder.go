// Package contextbuilder decides what tend injects into a harness thread
// before a turn (D17, D18): a full snapshot when the thread's model has no
// reliable view of the ledger, a delta of relevant changes otherwise, or
// nothing when nothing relevant changed.
//
// A packet is stored before it is injected and only advances the thread's
// watermark once injection and the turn that follows both succeeded. Any
// failure, or a packet a crash left unconfirmed, invalidates the baseline,
// so the next turn gets a new full snapshot rather than a replay: the
// harness does not deduplicate injections (spike A2).
package contextbuilder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Config tunes the builder. Zero values select the defaults.
type Config struct {
	// FullBudget and DeltaBudget bound packet sizes in characters; the
	// defaults are about 4k and 1.5k tokens.
	FullBudget  int
	DeltaBudget int
	// FocusMessages is how many recent messages a full snapshot recalls.
	FocusMessages int
}

func (c Config) withDefaults() Config {
	if c.FullBudget <= 0 {
		c.FullBudget = 16000
	}
	if c.DeltaBudget <= 0 {
		c.DeltaBudget = 6000
	}
	if c.FocusMessages <= 0 {
		c.FocusMessages = 8
	}
	return c
}

// relevant are the event kinds that change what the main agent should
// know. Progress, heartbeats and logs never are.
var relevant = []string{
	"task.created", "task.spec_updated", "task.completed", "task.cancelled", "task.control_changed", "task.priority_changed",
	"task.implement_requested", "task.acceptance_requested", "artifact.submitted", "acceptance.submitted",
	"run.started", "run.failed", "run.unknown", "run.lost", "run.reconciled",
	"decision.opened", "decision.answered", "outbox.failed", "outbox.unknown",
}

// Builder prepares packets for threads.
type Builder struct {
	app   *app.App
	store *store.Store
	ids   protocol.IDGen
	cfg   Config
}

// New returns a Builder. The store is used for reads only.
func New(a *app.App, s *store.Store, ids protocol.IDGen, cfg Config) *Builder {
	return &Builder{app: a, store: s, ids: ids, cfg: cfg.withDefaults()}
}

// Packet is a packet ready to inject.
type Packet struct {
	ID   string
	Text string
	Full bool
}

// Prepare returns the packet to inject before the turn that handles the
// conversation message with sequence number messageSeq, or nil when the
// thread's model is already current.
func (b *Builder) Prepare(ctx context.Context, threadKey string, messageSeq int64) (*Packet, error) {
	if err := b.settleUnconfirmed(ctx, threadKey); err != nil {
		return nil, err
	}
	var (
		snap   Snapshot
		empty  bool
		packet store.Packet
	)
	err := b.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		base, err := store.GetBaseline(ctx, q, threadKey)
		hasBase := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		latest, err := store.LatestEventSeq(ctx, q)
		if err != nil {
			return err
		}
		snap = Snapshot{Full: !hasBase || !base.Valid, Epoch: base.Epoch, Watermark: latest}
		from := base.Watermark
		if snap.Full {
			snap.Epoch++
			from = 0
			err = b.gatherFull(ctx, q, &snap, messageSeq)
		} else {
			err = b.gatherDelta(ctx, q, &snap, base.Watermark, latest)
			empty = len(snap.Changes) == 0 && len(snap.Decisions) == 0
		}
		packet = store.Packet{ThreadKey: threadKey, Epoch: snap.Epoch, FromSeq: from + 1, ToSeq: latest}
		return err
	})
	if err != nil || empty {
		return nil, err
	}
	snap.PacketID = b.ids.New(protocol.PrefixPacket)
	budget, kind := b.cfg.DeltaBudget, "delta"
	if snap.Full {
		budget, kind = b.cfg.FullBudget, "full"
	}
	packet.PacketID, packet.Kind, packet.Body = snap.PacketID, kind, Render(snap, budget)
	if err := b.app.RecordPacket(ctx, packet); err != nil {
		return nil, err
	}
	return &Packet{ID: packet.PacketID, Text: packet.Body, Full: snap.Full}, nil
}

// settleUnconfirmed marks a packet a previous attempt left neither
// confirmed nor failed, for example because tend stopped while injecting,
// as unknown; that invalidates the baseline.
func (b *Builder) settleUnconfirmed(ctx context.Context, threadKey string) error {
	var stale string
	err := b.store.Read(ctx, func(tx *store.ReadTx) error {
		p, err := store.LatestPacket(ctx, tx.Q(), threadKey)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err == nil && p.Status == store.PacketBuilt {
			stale = p.PacketID
		}
		return err
	})
	if err != nil || stale == "" {
		return err
	}
	return b.app.FailPacket(ctx, stale, true)
}

// Confirm records that the packet was injected and the turn after it
// started.
func (b *Builder) Confirm(ctx context.Context, packetID string) error {
	return b.app.ConfirmPacket(ctx, packetID)
}

// Fail records that the packet was not injected, or that it is unknown
// whether it was.
func (b *Builder) Fail(ctx context.Context, packetID string, unknown bool) error {
	return b.app.FailPacket(ctx, packetID, unknown)
}

// Invalidate makes the thread's next turn start from a full snapshot, for
// example after the harness compacted the thread.
func (b *Builder) Invalidate(ctx context.Context, threadKey string) error {
	return b.app.InvalidateBaseline(ctx, threadKey)
}

func (b *Builder) gatherFull(ctx context.Context, q store.Querier, s *Snapshot, messageSeq int64) error {
	decisions, err := store.ListDecisions(ctx, q, "")
	if err != nil {
		return err
	}
	for _, d := range decisions {
		s.Decisions = append(s.Decisions, decisionLine(d))
	}
	tasks, err := store.ListTasks(ctx, q, 500)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Lifecycle.Terminal() {
			continue
		}
		line, attention, err := taskLine(ctx, q, t)
		if err != nil {
			return err
		}
		if attention {
			s.Attention = append(s.Attention, line)
		} else {
			s.Tasks = append(s.Tasks, line)
		}
	}
	if messageSeq <= 1 {
		return nil
	}
	msgs, err := store.RecentMessages(ctx, q, b.cfg.FocusMessages, messageSeq-1)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		s.Focus = append(s.Focus, messageLine(m))
	}
	return nil
}

func (b *Builder) gatherDelta(ctx context.Context, q store.Querier, s *Snapshot, after, through int64) error {
	events, err := store.ListEventsAfter(ctx, q, after, through, relevant)
	if err != nil {
		return err
	}
	touched := map[string]bool{}
	var order []string
	for _, e := range events {
		var body map[string]any
		_ = json.Unmarshal([]byte(e.BodyJSON), &body)
		if body["stale"] == true {
			continue // recorded as a fact, changes nothing
		}
		taskID, err := taskOf(ctx, q, e, body)
		if err != nil {
			return err
		}
		switch e.Kind {
		case "decision.opened", "decision.answered":
			// Questions and answers go in the protected section, not
			// among changes that give way first.
			d, err := store.GetDecision(ctx, q, e.ObjectID)
			if err != nil {
				return err
			}
			line := "opened: " + decisionLine(d)
			if e.Kind == "decision.answered" {
				line = fmt.Sprintf("answered: %s %s -> %s", d.DecisionID, d.Question, d.AnswerJSON)
			}
			s.Decisions = append(s.Decisions, fmt.Sprintf("#%d %s", e.Seq, line))
		default:
			about := e.ObjectID
			if taskID != "" && taskID != e.ObjectID {
				about = e.ObjectID + " of " + taskID
			}
			s.Changes = append(s.Changes, strings.TrimSpace(fmt.Sprintf("#%d %s %s %s", e.Seq, about, e.Kind, compact(body))))
		}
		if taskID != "" && !touched[taskID] {
			touched[taskID] = true
			order = append(order, taskID)
		}
	}
	for _, id := range order {
		t, err := store.GetTask(ctx, q, id)
		if err != nil {
			return err
		}
		line, _, err := taskLine(ctx, q, t)
		if err != nil {
			return err
		}
		s.Changes = append(s.Changes, "now: "+line)
	}
	return nil
}

// taskOf finds the task an event concerns, through its run for run
// events.
func taskOf(ctx context.Context, q store.Querier, e store.EventView, body map[string]any) (string, error) {
	switch e.ObjectType {
	case "task":
		return e.ObjectID, nil
	case "run":
		r, err := store.GetRun(ctx, q, e.ObjectID)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return r.TaskID, err
	}
	id, _ := body["task_id"].(string)
	return id, nil
}

// taskLine describes a task and reports whether it needs attention.
func taskLine(ctx context.Context, q store.Querier, t store.TaskRow) (string, bool, error) {
	var spec struct {
		Scope       string   `json:"scope"`
		Constraints []string `json:"constraints"`
	}
	body, err := store.GetSpec(ctx, q, t.TaskID, t.SpecVersion)
	if err != nil {
		return "", false, err
	}
	_ = json.Unmarshal([]byte(body), &spec)
	runs, err := store.ListRunsByTask(ctx, q, t.TaskID)
	if err != nil {
		return "", false, err
	}
	var unknown []string
	for _, r := range runs {
		if r.ObservedState == protocol.RunUnknown {
			unknown = append(unknown, r.RunID)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %q: %s, priority %d, control %s rev %d, spec v%d, workspace %s",
		t.TaskID, t.Title, t.Lifecycle, t.Priority, t.ControlMode, t.ControlRevision, t.SpecVersion, t.WorkspaceName)
	if t.BlockReason != protocol.BlockNone {
		fmt.Fprintf(&b, "; blocked: %s", t.BlockReason)
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "; runs in unknown state: %s", strings.Join(unknown, ", "))
	}
	if spec.Scope != "" {
		fmt.Fprintf(&b, "; scope: %s", spec.Scope)
	}
	if len(spec.Constraints) > 0 {
		fmt.Fprintf(&b, "; constraints: %s", strings.Join(spec.Constraints, " | "))
	}
	return b.String(), t.BlockReason != protocol.BlockNone || len(unknown) > 0, nil
}

func decisionLine(d store.DecisionRow) string {
	var options []string
	_ = json.Unmarshal([]byte(d.OptionsJSON), &options)
	task := ""
	if d.TaskID != "" {
		task = " about " + d.TaskID
	}
	line := fmt.Sprintf("%s v%d%s: %s", d.DecisionID, d.Version, task, d.Question)
	if len(options) > 0 {
		line += " Options: " + strings.Join(options, " / ")
	}
	return line
}

func messageLine(m store.MessageView) string {
	text := m.Body
	if r := []rune(text); len(r) > 600 {
		text = string(r[:600]) + "…"
	}
	meta := []string{m.Author}
	if m.ReplyTo != "" {
		meta = append(meta, "reply to "+m.ReplyTo)
	}
	if len(m.Bindings) > 0 {
		meta = append(meta, "about "+strings.Join(m.Bindings, ", "))
	}
	return fmt.Sprintf("%s (%s): %s", m.MessageID, strings.Join(meta, "; "), text)
}

// compact renders an event body's scalar fields briefly.
func compact(body map[string]any) string {
	keys := make([]string, 0, len(body))
	for k, v := range body {
		switch v.(type) {
		case string, float64, bool:
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, body[k]))
	}
	out := strings.Join(parts, " ")
	if len(out) > 240 {
		out = out[:240] + "…"
	}
	return out
}
