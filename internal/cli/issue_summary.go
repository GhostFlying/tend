package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"text/tabwriter"

	"github.com/GhostFlying/tend/internal/protocol"
)

func printIssuePage(e *Env, p protocol.IssuePage) error {
	w := tabwriter.NewWriter(e.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ISSUE\tTITLE\tSTATE\tOWNER")
	for _, v := range p.Items {
		fmt.Fprintf(w, "%s\t%s\t%s / %s\t%s\n", v.IssueID, v.Title, v.State, v.BoardStatus(), v.Assignee.Ref)
		if v.Board != nil {
			fmt.Fprintf(w, "  Current responsibility: %d open actions; %d need the human owner\n", v.Board.AttentionCount, v.Board.OwnerAttentionCount)
			if v.Board.Progress != nil && v.Board.Progress.NextStep != "" {
				fmt.Fprintf(w, "  Next step (assessment at revision %d): %s\n", v.Board.Progress.IssueRevision, v.Board.Progress.NextStep)
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
func printIssueView(e *Env, v protocol.IssueView) error {
	out := e.Stdout
	fmt.Fprintf(out, "%s  %s\nState: %s / %s; owner: %s; revision: %d; epoch: %d\n\nGoal: %s\n", v.IssueID, v.Plan.Title, v.State, v.BoardStatus(), v.Assignee.Ref, v.Revision, v.AssignmentEpoch, v.Plan.Intent)
	if v.Manager != nil {
		fmt.Fprintf(out, "Owner session: %s; task: %s\n", v.Manager.State, v.Manager.TaskID)
	}
	if v.Children != nil {
		printIssueChildren(out, v.IssueID, v.Children)
	}
	if v.Progress != nil {
		fmt.Fprintf(out, "Current result: %s\nNext step: %s\n", v.Progress.Summary, v.Progress.NextStep)
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
		fmt.Fprintf(out, "    %s  %s  [%s; %d running; %d accepting; %d unknown; %d waiting_owner; %d waiting_supervisor]\n", child.IssueID, child.Title, child.Status, child.Execution.Running, child.Execution.Accepting, child.Execution.Unknown, child.Execution.WaitingOwner, child.Execution.WaitingSupervisor)
	}
	fmt.Fprintf(out, "  All children: tend issue list --parent %s --include-closed (use --limit / --after for pages; --json for full execution facts)\n", id)
}

func runIssueReadDetail(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "issue "+args[0])
	after := fs.String("after", "", "result cursor")
	before := fs.Int64("before", 0, "activity cursor")
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
	return readOwnerJSON(ctx, owner{e: e, c: c, json: true}, "/api/v1/issues/"+url.PathEscape(pos[0])+"/"+args[0]+"?"+query.Encode())
}
