package contextbuilder

import (
	"fmt"
	"strings"
)

// unratedTitle heads the tasks that need a rating, saying what to do.
const unratedTitle = "Unrated, ask the owner to rate them (p0 urgent, p1 high, p2 normal, p3 low) when it fits"

// Snapshot is what a packet says, gathered from the ledger.
type Snapshot struct {
	// PacketID is empty for a snapshot that is shown but not recorded.
	PacketID  string
	Epoch     int64
	Watermark int64
	// Full marks a full snapshot; otherwise the packet is a delta.
	Full bool
	// Addon marks context for an agent the owner works in rather than the
	// hosted main agent: a full snapshot then carries the standing
	// instructions, which such an agent has no other place for.
	Addon bool
	// Project says, in a full snapshot, which project the session works
	// in, when tend has more than one, and Others sums up what the other
	// projects hold.
	Project string
	Others  []string
	// Decisions are the open questions for the owner, in a full snapshot.
	Decisions []string
	// Attention lists, in a full snapshot, the tasks that are blocked or
	// have a run in unknown state, with their constraints; in a delta, the
	// changes that need someone to act, such as a new decision or a failed
	// run.
	Attention []string
	// Focus is the recent conversation, oldest first. It comes from the
	// messages, never from background events (v0.1 §6.1).
	Focus []string
	// Tasks lists the other unfinished tasks, most important first.
	Tasks []string
	// Results are the task outcomes in a delta.
	Results []string
	// Unseen lists, in a full snapshot, what the session has not heard of
	// and the snapshot cannot show: tasks that ended and decisions that
	// were answered since its watermark. UnseenAttention holds what needs
	// attention since then, such as a new decision or an answer tend
	// could not carry out; like a delta's attention, it is never cut for
	// the budget.
	Unseen          []string
	UnseenAttention []string
	// Changes are a delta's other changes to what the ledger wants, and
	// the current state of the tasks the delta touched.
	Changes []string
	// OtherAttention is, for a session that works in one project, what
	// needs attention in the others; like Attention, it is never cut.
	OtherAttention []string
	// Unrated lists the unfinished tasks no one has rated (D60): in a full
	// snapshot all of them, in a delta those others just created.
	Unrated []string
}

func (s Snapshot) empty() bool {
	return len(s.Attention) == 0 && len(s.Results) == 0 && len(s.Changes) == 0 && len(s.OtherAttention) == 0
}

// section is a block of lines with a priority; under budget pressure the
// lowest priority goes first, whole lines from the end, and an omission
// note points to the tools. A protected section is never cut.
type section struct {
	title     string
	lines     []string
	priority  int
	protected bool
	omitted   string // note format with one %d for the count
}

// Render writes a packet's text within budget characters. A line is never
// cut: a constraint is either there in full or its task is listed among
// the omissions.
func Render(s Snapshot, budget int) string {
	kind := "delta"
	if s.Full {
		kind = "full"
	}
	var header string
	if s.PacketID == "" {
		header = fmt.Sprintf("[tend context snapshot watermark=%d]\n", s.Watermark)
	} else {
		header = fmt.Sprintf("[tend context packet %s epoch=%d watermark=%d kind=%s]\n", s.PacketID, s.Epoch, s.Watermark, kind)
	}
	switch {
	case s.Full && s.Addon:
		header += "This replaces all earlier tend context in this conversation, including any carried over in a summary. " +
			"It is tend's ledger as of now and is authoritative over anything you remember.\n\n" + AddonInstructions + "\n"
		if s.Project != "" {
			header += "\n" + s.Project + "\n"
		}
		if len(s.Others) > 0 {
			header += "Other projects: " + strings.Join(s.Others, "; ") + ".\n"
		}
		if len(s.Tasks) == 0 && len(s.Attention) == 0 {
			header += "\nThere are no unfinished tasks.\n"
		}
	case s.Full:
		header += "This is tend's ledger as of now. It is authoritative over anything you remember; read details with the tend tools.\n"
	case s.Addon:
		header += "Changes in tend's ledger since your last tend context, made by others or by tend itself. Read details with the tend tools.\n"
	default:
		header += "Changes in tend's ledger since your last context. Background changes do not change what the owner is talking about.\n"
	}
	var sections []*section
	if s.Full {
		sections = []*section{
			{title: "Decisions", lines: s.Decisions, priority: 6, omitted: "(%d more decisions; see tasks_read)"},
			{title: "Needs attention", lines: s.Attention, priority: 5, omitted: "(%d more tasks need attention; see tasks_list)"},
			{title: "Recent conversation", lines: s.Focus, priority: 4, omitted: "(%d earlier messages omitted)"},
			{title: "Since your last tend context, needs attention", lines: s.UnseenAttention, protected: true},
			{title: "Since your last tend context", lines: s.Unseen, priority: 3, omitted: "(%d more; see tasks_list)"},
			{title: "Open tasks", lines: s.Tasks, priority: 2, omitted: "(%d more open tasks; see tasks_list)"},
			{title: unratedTitle, lines: s.Unrated, priority: 3, omitted: "(%d more unrated tasks; see tasks_list)"},
		}
	} else {
		sections = []*section{
			{title: "Needs attention", lines: s.Attention, protected: true},
			{title: "Other projects, needs attention", lines: s.OtherAttention, protected: true},
			{title: "Results", lines: s.Results, priority: 3, omitted: "(%d more results; see tasks_list)"},
			{title: "Changes", lines: s.Changes, priority: 2, omitted: "(%d more changes; see tasks_list)"},
			{title: unratedTitle, lines: s.Unrated, priority: 3, omitted: "(%d more unrated tasks; see tasks_list)"},
		}
	}
	focus := func(sec *section) bool { return sec.title == "Recent conversation" }
	dropped := map[*section]int{}
	size := func() int {
		n := len(header)
		for _, sec := range sections {
			if len(sec.lines) == 0 && dropped[sec] == 0 {
				continue
			}
			n += len(sec.title) + 4
			for _, l := range sec.lines {
				n += len(l) + 3
			}
			if dropped[sec] > 0 {
				n += len(sec.omitted) + 8
			}
		}
		return n
	}
	for size() > budget {
		var victim *section
		for _, sec := range sections {
			if !sec.protected && len(sec.lines) > 0 && (victim == nil || sec.priority < victim.priority) {
				victim = sec
			}
		}
		if victim == nil {
			break
		}
		if focus(victim) {
			// Drop the oldest messages first; keep the latest exchange.
			victim.lines = victim.lines[1:]
		} else {
			victim.lines = victim.lines[:len(victim.lines)-1]
		}
		dropped[victim]++
	}
	var b strings.Builder
	b.WriteString(header)
	for _, sec := range sections {
		if len(sec.lines) == 0 && dropped[sec] == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n", sec.title)
		if focus(sec) && dropped[sec] > 0 {
			fmt.Fprintf(&b, "- "+sec.omitted+"\n", dropped[sec])
		}
		for _, l := range sec.lines {
			fmt.Fprintf(&b, "- %s\n", l)
		}
		if !focus(sec) && dropped[sec] > 0 {
			fmt.Fprintf(&b, "- "+sec.omitted+"\n", dropped[sec])
		}
	}
	return b.String()
}
