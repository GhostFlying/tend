package cli

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"text/tabwriter"

	"github.com/GhostFlying/tend/internal/protocol"
)

func printIssuePage(e *Env, p protocol.IssuePage) error {
	w := tabwriter.NewWriter(e.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ISSUE\tTITLE\tSTATE\tOWNER")
	for _, v := range p.Items {
		fmt.Fprintf(w, "%s\t%s\t%s / %s\t%s\n", v.IssueID, v.Title, v.State, v.WorkStatus, v.Assignee.Ref)
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
	fmt.Fprintf(out, "%s  %s\nState: %s / %s; owner: %s; revision: %d; epoch: %d\n\nGoal: %s\n", v.IssueID, v.Plan.Title, v.State, v.WorkStatus, v.Assignee.Ref, v.Revision, v.AssignmentEpoch, v.Plan.Intent)
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
	fmt.Fprintln(out, "\nUse --json for the full plan, evidence, history links and execution facts.")
	return nil
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
