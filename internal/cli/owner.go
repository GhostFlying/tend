package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/GhostFlying/tend/internal/protocol"
)

// runOwner runs one of the owner's commands.
func runOwner(ctx context.Context, e *Env, cmd string, args []string) error {
	fs := newFlags(e, cmd)
	asJSON := fs.Bool("json", false, "print the server's answer as JSON")
	var follow *bool
	var note, kind *string
	switch cmd {
	case "logs":
		follow = fs.Bool("f", false, "keep following new logs until the run ends")
	case "decide":
		note = fs.String("note", "", "a free-form answer, with or instead of an option")
	case "retry":
		kind = fs.String("kind", "implement", "implement or accept")
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	want := map[string]int{"status": 0, "decide": -1}[cmd]
	if cmd != "status" && cmd != "decide" {
		want = 1
	}
	if want >= 0 && len(pos) != want {
		return usagef("%s takes %d argument(s)", cmd, want)
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: *asJSON}
	switch cmd {
	case "status":
		return o.status(ctx)
	case "task":
		return o.task(ctx, pos[0])
	case "logs":
		return o.logs(ctx, pos[0], *follow)
	case "decide":
		if len(pos) < 1 || len(pos) > 2 || (len(pos) == 1 && *note == "") {
			return usagef("decide takes a decision ID and an option, a --note, or both")
		}
		option := ""
		if len(pos) == 2 {
			option = pos[1]
		}
		return o.decide(ctx, pos[0], option, *note)
	case "retry":
		return o.retry(ctx, pos[0], protocol.ActionKind(*kind))
	case "pause":
		return o.setMode(ctx, pos[0], protocol.ControlPaused)
	case "resume":
		return o.setMode(ctx, pos[0], protocol.ControlManaged)
	default:
		return o.cancel(ctx, pos[0])
	}
}

type owner struct {
	e    *Env
	c    *client
	json bool
}

func (o owner) printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(o.e.Stdout, "%s\n", b)
	return err
}

func (o owner) status(ctx context.Context) error {
	var s protocol.Snapshot
	if err := o.c.do(ctx, "GET", "/api/v1/snapshot", nil, &s); err != nil {
		return err
	}
	if o.json {
		return o.printJSON(s)
	}
	w := tabwriter.NewWriter(o.e.Stdout, 0, 4, 2, ' ', 0)
	if len(s.Tasks) == 0 {
		fmt.Fprintln(w, "No unfinished tasks.")
	} else {
		fmt.Fprintln(w, "TASK\tTITLE\tSTATE\tCONTROL\tBLOCKED")
		for _, t := range s.Tasks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s r%d\t%s\n", t.TaskID, t.Title, t.Lifecycle, t.ControlMode, t.ControlRevision, blocked(t.BlockReason))
		}
	}
	w.Flush()
	if len(s.Decisions) > 0 {
		fmt.Fprintln(o.e.Stdout, "\nOpen decisions:")
		for _, d := range s.Decisions {
			printDecision(o.e.Stdout, d)
		}
	}
	if len(s.Hosts) > 0 {
		fmt.Fprintln(o.e.Stdout, "\nHosts:")
		for _, h := range s.Hosts {
			fmt.Fprintf(o.e.Stdout, "  %s (%s/%s): %d of %d slots busy\n", h.HostID, h.OS, h.Arch, h.OpenRuns, h.Slots)
		}
	}
	return nil
}

func blocked(b protocol.BlockReason) string {
	if b == protocol.BlockNone || b == "" {
		return "-"
	}
	return string(b)
}

func printDecision(w io.Writer, d protocol.DecisionView) {
	var options []string
	_ = json.Unmarshal(d.Options, &options)
	about := ""
	if d.TaskID != "" {
		about = " about " + d.TaskID
	}
	fmt.Fprintf(w, "  %s v%d%s [%s]: %s\n", d.DecisionID, d.Version, about, d.Status, d.Question)
	if len(options) > 0 {
		fmt.Fprintf(w, "    options: %s\n", strings.Join(options, " | "))
	}
	if len(d.Answer) > 0 {
		fmt.Fprintf(w, "    answer: %s\n", d.Answer)
	}
}

func (o owner) readTask(ctx context.Context, id string) (protocol.TaskDetail, error) {
	var d protocol.TaskDetail
	err := o.c.do(ctx, "GET", "/api/v1/tasks/"+url.PathEscape(id), nil, &d)
	return d, err
}

func (o owner) task(ctx context.Context, id string) error {
	d, err := o.readTask(ctx, id)
	if err != nil {
		return err
	}
	if o.json {
		return o.printJSON(d)
	}
	out := o.e.Stdout
	printSummary(out, d.TaskSummary)
	var spec struct {
		Goal       string   `json:"goal"`
		Acceptance []string `json:"acceptance"`
	}
	_ = json.Unmarshal(d.Spec, &spec)
	fmt.Fprintf(out, "goal: %s\n", spec.Goal)
	for _, a := range spec.Acceptance {
		fmt.Fprintf(out, "  accept if: %s\n", a)
	}
	if ws := d.WorkspaceState; ws != nil {
		fmt.Fprintf(out, "workspace on %s (%s)\n", ws.Host, ws.Status)
		for _, m := range ws.Mounts {
			fmt.Fprintf(out, "  %s: %s from %s at %s, branch %s\n", m.Path, m.Repo, m.ResolvedFrom, short(m.ResolvedBaseSHA), m.Branch)
		}
	}
	if len(d.Actions) > 0 {
		fmt.Fprintln(out, "actions:")
		for _, a := range d.Actions {
			fmt.Fprintf(out, "  %s %s gen %d spec v%d: %s\n", a.ActionID, a.Kind, a.Generation, a.SpecVersion, a.Status)
		}
	}
	if len(d.Runs) > 0 {
		fmt.Fprintln(out, "runs:")
		for _, r := range d.Runs {
			fmt.Fprintf(out, "  %s %s gen %d on %s: %s\n", r.RunID, r.Role, r.Generation, r.Host, r.Observed)
		}
	}
	if len(d.Artifacts) > 0 {
		fmt.Fprintln(out, "artifacts:")
		for _, a := range d.Artifacts {
			fmt.Fprintf(out, "  %s from %s\n", a.ArtifactID, a.RunID)
			var ref struct {
				Mounts []struct {
					Path      string `json:"path"`
					Branch    string `json:"branch"`
					BaseSHA   string `json:"base_sha"`
					CommitSHA string `json:"commit_sha"`
					Changed   bool   `json:"changed"`
				} `json:"mounts"`
			}
			_ = json.Unmarshal(a.Ref, &ref)
			for _, m := range ref.Mounts {
				if m.Changed {
					fmt.Fprintf(out, "    %s: %s at %s (base %s)\n", m.Path, m.Branch, short(m.CommitSHA), short(m.BaseSHA))
				} else {
					// Untouched in this run, but an earlier run may have
					// committed to it, so show what acceptance saw.
					at := m.CommitSHA
					if at == "" {
						at = m.BaseSHA
					}
					fmt.Fprintf(out, "    %s: unchanged in this run, at %s\n", m.Path, short(at))
				}
			}
		}
	}
	for _, a := range d.Acceptances {
		fmt.Fprintf(out, "verdict on %s (spec v%d): %s\n", a.ArtifactID, a.SpecVersion, a.Verdict)
		var issues []any
		_ = json.Unmarshal(a.Issues, &issues)
		for _, i := range issues {
			if s, ok := i.(string); ok {
				fmt.Fprintf(out, "  - %s\n", s)
			} else {
				b, _ := json.Marshal(i)
				fmt.Fprintf(out, "  - %s\n", b)
			}
		}
	}
	if len(d.Decisions) > 0 {
		fmt.Fprintln(out, "decisions:")
		for _, x := range d.Decisions {
			printDecision(out, x)
		}
	}
	return nil
}

func printSummary(w io.Writer, t protocol.TaskSummary) {
	fmt.Fprintf(w, "%s %q: %s, control %s at revision %d, spec v%d, priority %d, workspace %s",
		t.TaskID, t.Title, t.Lifecycle, t.ControlMode, t.ControlRevision, t.SpecVersion, t.Priority, t.Workspace)
	if t.BlockReason != protocol.BlockNone && t.BlockReason != "" {
		fmt.Fprintf(w, ", blocked: %s", t.BlockReason)
	}
	fmt.Fprintln(w)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func (o owner) logs(ctx context.Context, runID string, follow bool) error {
	var after int64
	for {
		wait := 0
		if follow {
			wait = 25
		}
		var page protocol.RunLogPage
		path := fmt.Sprintf("/api/v1/runs/%s/logs?after=%d&wait=%d", url.PathEscape(runID), after, wait)
		if err := o.c.do(ctx, "GET", path, nil, &page); err != nil {
			return err
		}
		for _, l := range page.Logs {
			if o.json {
				if err := json.NewEncoder(o.e.Stdout).Encode(l); err != nil {
					return err
				}
				continue
			}
			if line, ok := logLine(l); ok {
				fmt.Fprintln(o.e.Stdout, line)
			}
		}
		after = page.Next
		ended := page.Observed == protocol.RunFinished || page.Observed == protocol.RunFailed
		// A full page means more may already be there.
		if len(page.Logs) == 0 && (!follow || ended) {
			if follow && !o.json {
				fmt.Fprintf(o.e.Stdout, "run %s %s\n", runID, page.Observed)
			}
			return nil
		}
	}
}

// write sends an owner write and explains a stale or failed answer. It
// never retries a stale write: the owner decides again.
func (o owner) write(ctx context.Context, op string, target protocol.Target, revision *int64, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := protocol.Envelope{RequestID: o.e.IDs.New(protocol.PrefixRequest), Operation: op, Target: target, ExpectedControlRevision: revision, Payload: b}
	var res protocol.APIResponse
	// A lost answer is resent once under the same request ID: the server
	// returns the recorded outcome instead of acting twice.
	err = o.c.do(ctx, "POST", "/api/v1/ops", env, &res)
	if isTransport(err) {
		err = o.c.do(ctx, "POST", "/api/v1/ops", env, &res)
	}
	if err != nil {
		return err
	}
	if o.json {
		if err := o.printJSON(res); err != nil {
			return err
		}
	}
	opr := res.Operation
	if opr == nil {
		return errors.New("the server sent no outcome")
	}
	if opr.Status == protocol.OpFailed {
		if res.Current != nil && !o.json {
			fmt.Fprintln(o.e.Stdout, "The task changed since it was read; it is now:")
			printSummary(o.e.Stdout, *res.Current)
		}
		return opr.Error
	}
	if !o.json {
		fmt.Fprintf(o.e.Stdout, "%s: %s (request %s)\n", op, opr.Status, opr.RequestID)
	}
	return nil
}

func (o owner) decide(ctx context.Context, id, option, note string) error {
	var s protocol.Snapshot
	if err := o.c.do(ctx, "GET", "/api/v1/snapshot", nil, &s); err != nil {
		return err
	}
	for _, d := range s.Decisions {
		if d.DecisionID == id {
			return o.write(ctx, "decisions.respond", protocol.Target{Type: "decision", ID: id}, nil,
				protocol.AnswerDecisionPayload{Version: d.Version, Option: option, Text: note})
		}
	}
	return fmt.Errorf("decision %s is not open", id)
}

func (o owner) retry(ctx context.Context, id string, kind protocol.ActionKind) error {
	d, err := o.readTask(ctx, id)
	if err != nil {
		return err
	}
	var generation int64
	switch kind {
	case protocol.ActionImplement:
		generation = d.NextImplementGeneration
	case protocol.ActionAccept:
		// The next acceptance of the artifact the latest one judged.
		artifact := ""
		for _, a := range d.Actions {
			if a.Kind == protocol.ActionAccept && a.SpecVersion == d.SpecVersion {
				artifact = a.ArtifactID
			}
		}
		if artifact == "" {
			return fmt.Errorf("task %s has no acceptance to retry", id)
		}
		for _, a := range d.Actions {
			if a.Kind == protocol.ActionAccept && a.SpecVersion == d.SpecVersion && a.ArtifactID == artifact && a.Generation > generation {
				generation = a.Generation
			}
		}
		generation++
	default:
		return usagef("--kind must be implement or accept")
	}
	rev := d.ControlRevision
	return o.write(ctx, "actions.create", protocol.Target{Type: "task", ID: id}, &rev, protocol.CreateActionPayload{Kind: kind, Generation: generation})
}

func (o owner) setMode(ctx context.Context, id string, mode protocol.ControlMode) error {
	d, err := o.readTask(ctx, id)
	if err != nil {
		return err
	}
	rev := d.ControlRevision
	return o.write(ctx, "control.set_mode", protocol.Target{Type: "task", ID: id}, &rev, protocol.SetControlModePayload{Mode: mode})
}

func (o owner) cancel(ctx context.Context, id string) error {
	d, err := o.readTask(ctx, id)
	if err != nil {
		return err
	}
	rev := d.ControlRevision
	return o.write(ctx, "tasks.cancel", protocol.Target{Type: "task", ID: id}, &rev, map[string]any{})
}
