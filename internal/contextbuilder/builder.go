// Package contextbuilder decides what tend tells a main agent about the
// ledger: a full snapshot when the agent has no reliable view of it, a
// delta of the changes it should hear about otherwise, or nothing (D17,
// D18, D47).
//
// For the hosted main agent, a packet is stored before it is injected and
// only advances the thread's watermark once injection and the turn that
// follows both succeeded. Any failure, or a packet a crash left
// unconfirmed, invalidates the baseline, so the next turn gets a new full
// snapshot rather than a replay: the harness does not deduplicate
// injections (spike A2). An agent the owner works in gets its context from
// its hooks, through the agent.hook command, which advances the watermark
// as it answers.
package contextbuilder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Config tunes the builder. Zero values select the defaults.
type Config struct {
	// FullBudget and DeltaBudget bound the hosted main agent's packet
	// sizes in characters; the defaults are about 4k and 1.5k tokens.
	FullBudget  int
	DeltaBudget int
	// HookFullBudget and HookDeltaBudget bound the context of agents the
	// owner works in, which their hooks limit (C5): about 8000 and 4000
	// characters by default.
	HookFullBudget  int
	HookDeltaBudget int
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
	if c.HookFullBudget <= 0 {
		c.HookFullBudget = 8000
	}
	if c.HookDeltaBudget <= 0 {
		c.HookDeltaBudget = 4000
	}
	if c.FocusMessages <= 0 {
		c.FocusMessages = 8
	}
	return c
}

// class is how a delta treats an event kind (plan C7 §4.3).
type class int

const (
	// progress is never told: run lifecycle, workspace and artifact
	// progress, heartbeats and logs. The agent reads them when it needs to.
	progress class = iota
	// intent is a change to what the ledger wants, possibly made by
	// another session or the owner.
	intent
	// result is a task outcome.
	result
	// attention needs the agent or the owner to act. It is never left out
	// of a delta for its budget.
	attention
)

var classes = map[string]class{
	"task.relations_updated": intent, "task.created": intent, "task.spec_updated": intent, "task.rating_changed": intent, "task.project_changed": intent, "task.control_changed": intent,
	"task.implement_requested": intent, "task.acceptance_requested": intent, "decision.answered": intent,
	"task.started_over": intent, "task.workspace_abandoned": intent, "task.workspace_available": intent,
	"task.completed": result, "task.cancelled": result,
	"decision.opened": attention, "run.failed": attention, "run.unknown": attention, "run.lost": attention, "task.workspace_unavailable": attention,
	"task.taken_over": attention,
	"outbox.failed":   attention, "outbox.unknown": attention,
	// A passed verdict is a result; any other needs attention.
	"acceptance.submitted": result,
	// Recorded before ratings (D60); a session may not have heard of it yet.
	"task.priority_changed": intent,
}

// told lists the event kinds a delta may tell.
var told = slices.Sorted(maps.Keys(classes))

func classify(kind string, body map[string]any) class {
	c := classes[kind]
	if kind == "acceptance.submitted" && body["verdict"] != string(protocol.VerdictPassed) {
		return attention
	}
	// An answer whose effect tend could not carry out is left to the agent.
	if answer, _ := body["answer"].(map[string]any); kind == "decision.answered" && answer != nil {
		if effect, _ := answer["effect"].(map[string]any); effect != nil && effect["applied"] != true {
			return attention
		}
	}
	return c
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
		// The hosted main agent sees every project.
		sc, _, err := scopeOf(ctx, q, protocol.Principal{})
		if err != nil {
			return err
		}
		if snap.Full {
			snap.Epoch++
			from = 0
			err = b.gatherFull(ctx, q, &snap, messageSeq, sc)
			if err == nil && hasBase && base.Watermark > 0 {
				// A completed task is absent from the active snapshot, and
				// a stale client may not have seen its notice yet. Preserve
				// outcomes since the last confirmed packet when rebuilding.
				var since Snapshot
				err = b.gatherDelta(ctx, q, &since, base.Watermark, latest, protocol.Principal{}, sc)
				snap.Unseen = since.Unseen
				snap.UnseenAttention = append(since.Attention, since.OtherAttention...)
			}
		} else {
			err = b.gatherDelta(ctx, q, &snap, base.Watermark, latest, protocol.Principal{}, sc)
			empty = snap.empty()
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

// Compose implements app.ComposeFunc: the context of an agent session the
// owner works in, read inside the hook's transaction.
func (b *Builder) Compose(ctx context.Context, q store.Querier, r app.ComposeRequest) (app.Composed, error) {
	latest, err := store.LatestEventSeq(ctx, q)
	if err != nil {
		return app.Composed{}, err
	}
	snap := Snapshot{PacketID: r.PacketID, Epoch: r.Epoch, Watermark: latest, Full: r.Full, Addon: true}
	budget := b.cfg.HookFullBudget
	if r.Full {
		var sc *scope
		sc, snap.Project, err = scopeOf(ctx, q, r.Self)
		if err == nil {
			err = b.gatherFull(ctx, q, &snap, 0, sc)
		}
		if err == nil && r.Watermark > 0 {
			// Tasks that ended and decisions answered since the session
			// last heard from tend are not in the snapshot; tell them. What
			// needs attention is told in full, as a delta would.
			var since Snapshot
			err = b.gatherDelta(ctx, q, &since, r.Watermark, latest, r.Self, sc)
			snap.Unseen, snap.UnseenAttention = since.Unseen, append(since.Attention, since.OtherAttention...)
		}
	} else {
		budget = b.cfg.HookDeltaBudget
		var sc *scope
		if sc, _, err = scopeOf(ctx, q, r.Self); err == nil {
			err = b.gatherDelta(ctx, q, &snap, r.Watermark, latest, r.Self, sc)
		}
	}
	if err != nil {
		return app.Composed{}, err
	}
	if !r.Full && snap.empty() {
		return app.Composed{ToSeq: latest}, nil
	}
	return app.Composed{Text: Render(snap, budget), ToSeq: latest}, nil
}

// Current returns a full snapshot for an agent session the owner works in
// without recording it or moving any watermark, for an agent that wants to
// refresh its view.
func (b *Builder) Current(ctx context.Context, self protocol.Principal) (string, error) {
	var out string
	err := b.store.Read(ctx, func(tx *store.ReadTx) error {
		c, err := b.Compose(ctx, tx.Q(), app.ComposeRequest{Self: self, Full: true})
		out = c.Text
		return err
	})
	return out, err
}

func (b *Builder) gatherFull(ctx context.Context, q store.Querier, s *Snapshot, messageSeq int64, sc *scope) error {
	tasks, err := store.ListTasks(ctx, q, sc.project, 500)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		sc.projects[t.TaskID] = t.Project
	}
	// What the other projects hold, counted for a summary.
	type count struct{ tasks, decisions int }
	others := map[string]*count{}
	other := func(project string) *count {
		if others[project] == nil {
			others[project] = &count{}
		}
		return others[project]
	}
	if sc.project != "" {
		open, err := store.CountOpenTasks(ctx, q)
		if err != nil {
			return err
		}
		for p, n := range open {
			if p != sc.project {
				other(p).tasks = n
			}
		}
	}
	decisions, err := store.ListDecisions(ctx, q, "")
	if err != nil {
		return err
	}
	for _, d := range decisions {
		p, err := sc.projectOf(ctx, q, d.TaskID)
		if err != nil {
			return err
		}
		if !sc.covers(p) {
			other(p).decisions++
			continue
		}
		s.Decisions = append(s.Decisions, sc.label(p, decisionLine(d)))
	}
	for _, t := range tasks {
		if t.Lifecycle.Terminal() {
			continue
		}
		line, attention, err := taskLine(ctx, q, t)
		if err != nil {
			return err
		}
		line = sc.label(t.Project, line)
		if attention {
			s.Attention = append(s.Attention, line)
		} else {
			s.Tasks = append(s.Tasks, line)
		}
		if t.Rating == protocol.RatingNone {
			s.Unrated = append(s.Unrated, sc.label(t.Project, unratedLine(t)))
		}
	}
	for _, p := range slices.Sorted(maps.Keys(others)) {
		c := others[p]
		s.Others = append(s.Others, fmt.Sprintf("%s: %d open tasks, %d open decisions", p, c.tasks, c.decisions))
	}
	single, err := app.SingleMountOnly(ctx, q)
	if err != nil {
		return err
	}
	if single {
		s.Limits = append(s.Limits, "No active host's worker supports a workspace with more than one mount: "+
			"give every task a single repository, and tasks_create refuses more.")
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

// gatherDelta collects the changes after the watermark that an agent
// should hear about. When self is an agent session, the events its own
// requests recorded are left out: it already has their results.
func (b *Builder) gatherDelta(ctx context.Context, q store.Querier, s *Snapshot, after, through int64, self protocol.Principal, sc *scope) error {
	events, err := store.ListEventsAfter(ctx, q, after, through, told)
	if err != nil {
		return err
	}
	own := ownRequests{self: self, seen: map[string]bool{}}
	titles := map[string]string{}
	touched := map[string]bool{}
	var order []string
	for _, e := range events {
		var body map[string]any
		_ = json.Unmarshal([]byte(e.BodyJSON), &body)
		if body["stale"] == true {
			continue // recorded as a fact, changes nothing
		}
		mine, err := own.made(ctx, q, e.RequestID)
		if err != nil {
			return err
		}
		if mine {
			continue
		}
		taskID, err := taskOf(ctx, q, e, body)
		if err != nil {
			return err
		}
		var line string
		switch e.Kind {
		case "decision.opened", "decision.answered":
			d, err := store.GetDecision(ctx, q, e.ObjectID)
			if err != nil {
				return err
			}
			if taskID == "" {
				taskID = d.TaskID
			}
			line = fmt.Sprintf("#%d opened: %s (by %s)", e.Seq, decisionLine(d), actor(e.ActorJSON))
			if e.Kind == "decision.answered" {
				line = fmt.Sprintf("#%d answered by %s: %s", e.Seq, actor(e.ActorJSON), answerLine(d))
			}
		default:
			about := e.ObjectID
			if taskID != "" {
				// Name the task, so a line reads without looking it up.
				title, ok := titles[taskID]
				if !ok {
					t, err := store.GetTask(ctx, q, taskID)
					if err != nil && !errors.Is(err, store.ErrNotFound) {
						return err
					}
					title, titles[taskID] = t.Title, t.Title
				}
				named := taskID
				if title != "" {
					named = fmt.Sprintf("%s %q", taskID, clipRunes(title, 80))
				}
				about = named
				if taskID != e.ObjectID {
					about = e.ObjectID + " of " + named
				}
			}
			line = strings.TrimSpace(fmt.Sprintf("#%d %s %s %s", e.Seq, about, e.Kind, compact(body)))
			if e.Kind == "acceptance.submitted" && body["verdict"] != string(protocol.VerdictPassed) {
				issues, err := reviewIssues(ctx, q, taskID, body)
				if err != nil {
					return err
				}
				if issues != "" {
					line += "; issues: " + issues
				}
			}
			line += " (by " + actor(e.ActorJSON) + ")"
		}
		c := classify(e.Kind, body)
		project, err := sc.projectOf(ctx, q, taskID)
		if err != nil {
			return err
		}
		line = sc.label(project, line)
		// A task moved away from the session's project is still news there.
		from, _ := body["from"].(string)
		if !sc.covers(project) && !(e.Kind == "task.project_changed" && sc.covers(from)) {
			// Of another project, only what needs attention is told.
			if c == attention {
				s.OtherAttention = append(s.OtherAttention, line)
			}
			continue
		}
		if e.Kind == "task.created" {
			// Someone else dispatched it; the owner may not have rated it.
			t, err := store.GetTask(ctx, q, taskID)
			if err != nil {
				return err
			}
			if t.Rating == protocol.RatingNone && !t.Lifecycle.Terminal() {
				s.Unrated = append(s.Unrated, sc.label(project, unratedLine(t)))
			}
		}
		if c != attention && (c == result || e.Kind == "decision.answered") {
			s.Unseen = append(s.Unseen, line)
		}
		switch c {
		case attention:
			s.Attention = append(s.Attention, line)
		case result:
			s.Results = append(s.Results, line)
		default:
			s.Changes = append(s.Changes, line)
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
		s.Changes = append(s.Changes, "now: "+sc.label(t.Project, line))
	}
	return nil
}

// actor names who caused an event, so an agent can tell the owner's
// deliberate changes from its own work and tend's. Runners, runs and
// tend's own rules are all tend.
func actor(principalJSON string) string {
	var p protocol.Principal
	if principalJSON == "" || json.Unmarshal([]byte(principalJSON), &p) != nil {
		return "tend"
	}
	switch {
	case p.Kind == protocol.PrincipalOwner:
		return "owner"
	case p.Kind == protocol.PrincipalMainAgent && p.Agent == "":
		return "the hosted main agent"
	case p.Kind == protocol.PrincipalMainAgent && p.Thread == "":
		return "a " + string(p.Agent) + " session"
	case p.Kind == protocol.PrincipalMainAgent:
		session := p.Thread
		if r := []rune(session); len(r) > 8 {
			session = string(r[:8])
		}
		return string(p.Agent) + " session " + session
	}
	return "tend"
}

// ownRequests tells whether a request was made by an agent session itself.
type ownRequests struct {
	self protocol.Principal
	seen map[string]bool
}

func (o ownRequests) made(ctx context.Context, q store.Querier, requestID string) (bool, error) {
	// Without its session, a call cannot be told apart from another
	// session's, so its events go to every session.
	if !o.self.Addon() || o.self.Thread == "" || requestID == "" {
		return false, nil
	}
	if mine, ok := o.seen[requestID]; ok {
		return mine, nil
	}
	rec, err := store.GetRequest(ctx, q, o.self.DedupDomain(), requestID)
	if errors.Is(err, store.ErrNotFound) {
		o.seen[requestID] = false
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var p protocol.Principal
	if err := json.Unmarshal([]byte(rec.PrincipalJSON), &p); err != nil {
		return false, fmt.Errorf("contextbuilder: decode principal of %s: %w", requestID, err)
	}
	mine := p.Kind == o.self.Kind && p.Agent == o.self.Agent && p.Thread == o.self.Thread
	o.seen[requestID] = mine
	return mine, nil
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

// unratedLine names a task that still needs a rating.
func unratedLine(t store.TaskRow) string {
	return fmt.Sprintf("%s %q, revision %d", t.TaskID, clipRunes(t.Title, 80), t.ControlRevision)
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
	fmt.Fprintf(&b, "%s %q: %s, %s, control %s rev %d, spec v%d, workspace %s",
		t.TaskID, t.Title, t.Lifecycle, t.Rating.Label(), t.ControlMode, t.ControlRevision, t.SpecVersion, t.WorkspaceName)
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
	refs, err := store.ListTaskRelations(ctx, q, t.TaskID)
	if err != nil {
		return "", false, err
	}
	for _, r := range refs {
		direction, other := "outgoing", r.TargetTaskID
		if r.TargetTaskID == t.TaskID {
			direction, other = "incoming", r.SourceTaskID
		}
		fmt.Fprintf(&b, "; %s %s: %s", r.Kind, direction, other)
	}
	return b.String(), t.BlockReason != protocol.BlockNone || len(unknown) > 0, nil
}

func decisionLine(d store.DecisionRow) string {
	var options []string
	_ = json.Unmarshal([]byte(d.OptionsJSON), &options)
	var effects map[string]protocol.DecisionEffect
	_ = json.Unmarshal([]byte(d.EffectsJSON), &effects)
	task := ""
	if d.TaskID != "" {
		task = " about " + d.TaskID
	}
	line := fmt.Sprintf("%s v%d%s: %s", d.DecisionID, d.Version, task, d.Question)
	if len(options) > 0 {
		for i, o := range options {
			if _, ok := effects[o]; ok {
				options[i] = o + " (tend carries it out)"
			}
		}
		line += " Options: " + strings.Join(options, " / ")
	}
	return line
}

// answerLine tells an answer and what tend did about it, so the agent
// neither repeats an effect tend applied nor misses one it did not.
func answerLine(d store.DecisionRow) string {
	var a struct {
		Option string                 `json:"option"`
		Text   string                 `json:"text"`
		Effect *protocol.EffectResult `json:"effect"`
	}
	_ = json.Unmarshal([]byte(d.AnswerJSON), &a)
	var answer []string
	if a.Option != "" {
		answer = append(answer, strconv.Quote(a.Option))
	}
	if a.Text != "" {
		answer = append(answer, "note "+strconv.Quote(a.Text))
	}
	line := fmt.Sprintf("%s %s -> %s", d.DecisionID, d.Question, strings.Join(answer, ", "))
	switch {
	case a.Effect == nil:
	case a.Effect.Applied:
		line += fmt.Sprintf("; tend carried it out (%s), nothing left to do", a.Effect.Effect)
	default:
		line += fmt.Sprintf("; tend did not carry it out (%s): %s. Act on the answer yourself", a.Effect.Effect, a.Effect.Reason)
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
// reviewIssues summarizes what an acceptance run asked to change, so an
// agent learns why a verdict was not passed without reading the task.
func reviewIssues(ctx context.Context, q store.Querier, taskID string, body map[string]any) (string, error) {
	runID, _ := body["run_id"].(string)
	artifactID, _ := body["artifact_id"].(string)
	if taskID == "" || runID == "" {
		return "", nil
	}
	accs, err := store.ListAcceptances(ctx, q, taskID, artifactID)
	if err != nil {
		return "", err
	}
	for _, a := range accs {
		if a.RunID != runID {
			continue
		}
		var issues []any
		if json.Unmarshal([]byte(a.IssuesJSON), &issues) != nil {
			return clipRunes(a.IssuesJSON, 400), nil
		}
		parts := make([]string, 0, len(issues))
		for _, i := range issues {
			if text, ok := i.(string); ok {
				parts = append(parts, strings.TrimSpace(text))
				continue
			}
			b, _ := json.Marshal(i)
			parts = append(parts, string(b))
		}
		return clipRunes(strings.Join(parts, " | "), 400), nil
	}
	return "", nil
}

// clipRunes shortens s to at most n runes, marking the cut.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

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

// scope is the part of the ledger a session's context covers (D59): the
// project the session works in, with only what needs attention from the
// others, or every project.
type scope struct {
	// project is the session's project; empty covers every project.
	project string
	// labels makes lines name their project, when tend has several.
	labels bool
	// projects caches the project of each task looked up.
	projects map[string]string
}

// scopeOf returns the scope of a session's context, and the line that
// tells the session about it when tend has several projects.
func scopeOf(ctx context.Context, q store.Querier, self protocol.Principal) (*scope, string, error) {
	sc := &scope{projects: map[string]string{}}
	all, err := store.ListProjects(ctx, q)
	if err != nil {
		return nil, "", err
	}
	var names []string
	for _, p := range all {
		if !p.Retired {
			names = append(names, p.Project)
		}
	}
	var sp store.SessionProject
	if self.Addon() && self.Thread != "" {
		if sp, err = store.GetSessionProject(ctx, q, string(self.Agent), self.Thread); err != nil {
			return nil, "", err
		}
	}
	if slices.Contains(names, sp.Project) {
		// The scope holds even with one active project: retired projects
		// may still have open tasks.
		sc.project = sp.Project
	}
	// Lines name their project whenever the context may hold more than
	// one, retired projects with open tasks included.
	open, err := store.CountOpenTasks(ctx, q)
	if err != nil {
		return nil, "", err
	}
	sc.labels = len(names) > 1 || sc.project != "" || len(open) > 1
	if len(names) <= 1 {
		return sc, "", nil
	}
	if sc.project == "" {
		// Every project, as chosen, by default, or because the session's
		// project was retired since.
		return sc, fmt.Sprintf("Projects: %s. This session sees all of them; when the owner works on one, "+
			"choose it with session_project.", strings.Join(names, ", ")), nil
	}
	how := "the project of the repository this session works in"
	if sp.Source == store.ProjectExplicit {
		how = "as chosen for this session"
	}
	others := slices.DeleteFunc(names, func(n string) bool { return n == sp.Project })
	return sc, fmt.Sprintf("Project: %s, %s. Only its tasks are shown, and from the other projects (%s) only what needs attention. "+
		"Switch with session_project when the owner asks; tasks_list with project \"*\" lists every project.",
		sp.Project, how, strings.Join(others, ", ")), nil
}

// projectOf returns the project of a task, or "" for none.
func (sc *scope) projectOf(ctx context.Context, q store.Querier, taskID string) (string, error) {
	if taskID == "" {
		return "", nil
	}
	if p, ok := sc.projects[taskID]; ok {
		return p, nil
	}
	t, err := store.GetTask(ctx, q, taskID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	sc.projects[taskID] = t.Project
	return t.Project, nil
}

// covers reports whether something in project belongs to the session's own
// context. What concerns no task belongs to every project.
func (sc *scope) covers(project string) bool {
	return sc.project == "" || project == "" || project == sc.project
}

// label names a line's project when lines name theirs and it is not the
// session's own.
func (sc *scope) label(project, line string) string {
	if !sc.labels || project == "" || project == sc.project {
		return line
	}
	return "[" + project + "] " + line
}
