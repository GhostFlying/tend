package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/GhostFlying/tend/internal/protocol"
)

func printIssuePage(e *Env, p protocol.IssuePage) error {
	w := tabwriter.NewWriter(e.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ISSUE\tINTERNAL ID\tTITLE\tSTATE\tOWNER")
	for _, v := range p.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s / %s\t%s\n", v.ShortCode, v.IssueID, v.Title, v.State, v.BoardStatus(), v.Assignee.Ref)
		execution := v.Execution
		if v.Board != nil {
			execution = &v.Board.Execution
		}
		if execution != nil {
			printIssueExecution(w, *execution)
		}
		if v.Board != nil {
			fmt.Fprintf(w, "  Current responsibility: %d open actions; %d need the human owner\n", v.Board.AttentionCount, v.Board.OwnerAttentionCount)
			if v.Board.Progress != nil && v.Board.Progress.NextStep != "" {
				label := "Next step"
				if v.Board.Progress.Historical {
					label = "Historical next step"
				}
				fmt.Fprintf(w, "  %s (assessment at revision %d): %s\n", label, v.Board.Progress.IssueRevision, v.Board.Progress.NextStep)
			}
		}
		if v.Board != nil && v.Board.Children != nil {
			printIssueChildren(w, v.IssueID, v.Board.Children)
		}
	}
	if len(p.Items) == 0 {
		fmt.Fprintln(w, "No matching Issues.")
	}
	if p.More {
		fmt.Fprintf(w, "Next page: --after %s\n", p.Next)
	}
	return w.Flush()
}
func printIssueExecution(out io.Writer, counts protocol.ExecutionCounts) {
	var states []string
	for _, state := range []struct {
		count int
		label string
	}{
		{counts.Running, "running"},
		{counts.Accepting, "accepting"},
		{counts.Starting, "starting"},
		{counts.Recovering, "recovering"},
		{counts.Queued, "queued"},
		{counts.WaitingExternal, "waiting for external conditions"},
		{counts.WaitingOwner, "waiting for the owner"},
		{counts.WaitingSupervisor, "waiting for the supervisor"},
		{counts.Paused, "paused"},
		{counts.Unknown, "unknown"},
	} {
		if state.count > 0 {
			states = append(states, fmt.Sprintf("%d %s", state.count, state.label))
		}
	}
	if len(states) > 0 {
		fmt.Fprintf(out, "  Execution: %s\n", strings.Join(states, "; "))
	}
}

func printIssueView(e *Env, v protocol.IssueView) error {
	out := e.Stdout
	fmt.Fprintf(out, "%s  %s\nState: %s / %s; owner: %s; revision: %d; epoch: %d\n\nGoal: %s\n", cmp.Or(v.ShortCode, v.IssueID), v.Plan.Title, v.State, v.BoardStatus(), v.Assignee.Ref, v.Revision, v.AssignmentEpoch, v.Plan.Intent)
	if v.ShortCode != "" {
		fmt.Fprintf(out, "Internal ID: %s\n", v.IssueID)
	}
	if v.Manager != nil {
		fmt.Fprintf(out, "Owner session: %s; task: %s\n", v.Manager.State, v.Manager.TaskID)
		printRunObservation(out, v.Manager.Observation)
	}
	if v.Children != nil {
		printIssueChildren(out, v.IssueID, v.Children)
	}
	if v.Progress != nil {
		if v.Progress.Historical {
			fmt.Fprintf(out, "Historical assessment (revision %d; %s): %s\nHistorical next step: %s\n", v.Progress.IssueRevision, v.Progress.HistoricalReason, v.Progress.Summary, v.Progress.NextStep)
		} else {
			fmt.Fprintf(out, "Current result: %s\nNext step: %s\n", v.Progress.Summary, v.Progress.NextStep)
		}
		if v.Progress.IssueRevision != v.Revision {
			fmt.Fprintln(out, "Progress describes an earlier Issue revision; recheck the current evidence.")
		}
	} else {
		fmt.Fprintln(out, "Current result: no assessment recorded.\nNext step: inspect sources, responsibility and execution blockers.")
	}
	fmt.Fprintf(out, "\nStages (%d):\n", len(v.Plan.Stages))
	for _, s := range v.Plan.Stages {
		fmt.Fprintf(out, "  %s\t%s\t%s\n", s.StageID, s.Title, s.Status)
	}
	fmt.Fprintf(out, "\nTasks: %d; deliveries: %d; follow-on responsibilities: %d; pending completion reviews: %d\n", len(v.Tasks), len(v.Plan.Deliveries), len(v.Plan.FollowOns), len(v.PendingReviews))
	for _, task := range v.Tasks {
		fmt.Fprintf(out, "  task %s  %s\n", task.TaskID, task.Title)
	}
	if v.Attentions != nil {
		fmt.Fprintln(out, "\nCurrent responsibility actions:")
		for _, a := range v.Attentions.Items {
			recipient := "owner"
			if a.Responsibility != nil {
				recipient = a.Responsibility.RecipientKind
			}
			fmt.Fprintf(out, "  %s  %s  [%s; origin: %s]\n", a.AttentionID, a.Title, recipient, a.IssueID)
		}
		if v.Attentions.More {
			fmt.Fprintf(out, "  More: tend attention list --responsible-issue %s --recipient all --status open --actionable-only --all --after %s\n", v.IssueID, v.Attentions.Next)
		}
	}
	fmt.Fprintln(out, "\nUse --json for the full plan, evidence, history links and execution facts.")
	return nil
}

func printIssueChildren(out io.Writer, id string, children *protocol.IssueChildProgress) {
	fmt.Fprintf(out, "  Children: %d/%d done; %d cancelled; %d executing\n", children.Done, children.Total, children.Cancelled, children.Running)
	for _, child := range children.Items {
		fmt.Fprintf(out, "    %s  %s  [%s; %d running; %d accepting; %d unknown; %d waiting_owner; %d waiting_supervisor; %d waiting_external]\n", child.IssueID, child.Title, child.Status, child.Execution.Running, child.Execution.Accepting, child.Execution.Unknown, child.Execution.WaitingOwner, child.Execution.WaitingSupervisor, child.Execution.WaitingExternal)
	}
	fmt.Fprintf(out, "  All children: tend issue list --parent %s --include-closed (use --limit / --after for pages; --json for full execution facts)\n", id)
}

func runIssueReadDetail(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "issue "+args[0])
	if help := auditReadHelp("issue " + args[0]); help != "" {
		fs.Usage = func() { fmt.Fprint(e.Stderr, help) }
	}
	after := fs.String("after", "", "result cursor")
	before := fs.Int64("before", 0, "activity cursor")
	var notification, since, through, from *int64
	var view *string
	if args[0] == "activity" {
		view = fs.String("view", "", "activity projection (readable omits detailed tool records)")
		since = fs.Int64("since", 0, "read activity changed after this event sequence")
		through = fs.Int64("through", 0, "event sequence to freeze across incremental pages")
		from = fs.Int64("from", 0, "oldest retained activity position to refresh")
		notification = fs.Int64("notification", 0, "read notification evidence for an activity event position; --after continues its pages")
	}
	fs.Bool("json", true, "print complete structured output")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("issue %s takes one Issue ID", args[0])
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	query := url.Values{"after": {*after}, "before": {strconv.FormatInt(*before, 10)}}
	path := "/api/v1/issues/" + url.PathEscape(pos[0]) + "/" + args[0]
	if view != nil {
		if *view != "" {
			query.Set("view", *view)
		}
		for key, value := range map[string]int64{"since": *since, "through": *through, "from": *from} {
			if value != 0 {
				query.Set(key, strconv.FormatInt(value, 10))
			}
		}
	}
	if notification != nil && *notification != 0 {
		if *notification < 0 || *before != 0 || *view != "" || *since != 0 || *through != 0 || *from != 0 {
			return usagef("--notification requires a positive event position and cannot be combined with activity projection or cursor flags")
		}
		path += "/" + strconv.FormatInt(*notification, 10) + "/notification"
		query.Del("before")
	}
	return readOwnerJSON(ctx, owner{e: e, c: c, json: true}, path+"?"+query.Encode())
}
