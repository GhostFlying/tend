package contextbuilder

import (
	"fmt"
	"strings"
)

// Snapshot is what a packet says, gathered from the ledger.
type Snapshot struct {
	PacketID  string
	Epoch     int64
	Watermark int64
	// Full marks a full snapshot; otherwise the packet is a delta.
	Full bool
	// Decisions are the open questions for the owner.
	Decisions []string
	// Attention lists tasks that are blocked or have a run in unknown
	// state, with their constraints.
	Attention []string
	// Focus is the recent conversation, oldest first. It comes from the
	// messages, never from background events (v0.1 §6.1).
	Focus []string
	// Tasks lists the other unfinished tasks, most important first.
	Tasks []string
	// Changes are the relevant ledger changes since the watermark; a
	// delta's decision questions and answers are in Decisions instead.
	Changes []string
}

// section is a block of lines with a priority; under budget pressure the
// lowest priority goes first, whole lines from the end, and an omission
// note points to the tools.
type section struct {
	title    string
	lines    []string
	priority int
	omitted  string // note format with one %d for the count
}

// Render writes a packet's text within budget characters. A line is never
// cut: a constraint is either there in full or its task is listed among
// the omissions.
func Render(s Snapshot, budget int) string {
	kind := "delta"
	if s.Full {
		kind = "full"
	}
	header := fmt.Sprintf("[tend context packet %s epoch=%d watermark=%d kind=%s]\n", s.PacketID, s.Epoch, s.Watermark, kind)
	if s.Full {
		header += "This is tend's ledger as of now. It is authoritative over anything you remember; read details with the tend tools.\n"
	} else {
		header += "Changes in tend's ledger since your last context. Background changes do not change what the owner is talking about.\n"
	}
	sections := []*section{
		{title: "Decisions", lines: s.Decisions, priority: 6, omitted: "(%d more decisions; see tasks_read)"},
		{title: "Needs attention", lines: s.Attention, priority: 5, omitted: "(%d more tasks need attention; see tasks_list)"},
		{title: "Recent conversation", lines: s.Focus, priority: 4, omitted: "(%d earlier messages omitted)"},
		{title: "Changes", lines: s.Changes, priority: 3, omitted: "(%d more changes; see tasks_read)"},
		{title: "Open tasks", lines: s.Tasks, priority: 2, omitted: "(%d more open tasks; see tasks_list)"},
	}
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
			if len(sec.lines) > 0 && (victim == nil || sec.priority < victim.priority) {
				victim = sec
			}
		}
		if victim == nil {
			break
		}
		if victim == sections[2] {
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
		if sec == sections[2] && dropped[sec] > 0 {
			fmt.Fprintf(&b, "- "+sec.omitted+"\n", dropped[sec])
		}
		for _, l := range sec.lines {
			fmt.Fprintf(&b, "- %s\n", l)
		}
		if sec != sections[2] && dropped[sec] > 0 {
			fmt.Fprintf(&b, "- "+sec.omitted+"\n", dropped[sec])
		}
	}
	return b.String()
}
