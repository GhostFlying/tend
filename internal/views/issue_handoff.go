package views

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// IssueHandoffPrompt reads a coherent ledger snapshot and never calls a provider.
func IssueHandoffPrompt(ctx context.Context, q store.Querier, issue, run string) (protocol.IssueHandoffPrompt, error) {
	var out protocol.IssueHandoffPrompt
	i, err := CanonicalIssue(ctx, q, issue, 0)
	if err != nil {
		return out, err
	}
	c, err := CanonicalIssueContext(ctx, q, issue, 0)
	if err != nil {
		return out, err
	}
	out = protocol.IssueHandoffPrompt{IssueID: issue, Title: i.Plan.Title, Revision: i.Revision, AssignmentEpoch: i.AssignmentEpoch,
		ContextRevision: c.Revision, Watermark: c.Watermark, ContextSyncStatus: c.SyncStatus, ContextGaps: c.Gaps, SourcesMore: c.More, NextSource: c.Next}
	quoted := handoffArgument(issue)
	out.Queries = []string{
		"tend deployment show --json",
		"tend issue show " + quoted + " --json",
		"tend issue context " + quoted + " --after-source 0 --json",
		"tend issue results " + quoted + " --json",
		"tend issue deliveries " + quoted + " --json",
		"tend issue activity " + quoted + " --json",
	}
	if i.Conversation != nil && i.Conversation.ConversationID != "" {
		out.ConversationID = i.Conversation.ConversationID
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM conversation_messages WHERE conversation_id=?`, out.ConversationID).Scan(&out.ConversationThrough); err != nil {
			return out, err
		}
		out.Queries = append(out.Queries, fmt.Sprintf("tend conversation history %s --before %d --limit 100 --json", handoffArgument(out.ConversationID), out.ConversationThrough+1))
	}
	if run != "" {
		r, err := store.GetRun(ctx, q, run)
		if err != nil {
			return out, err
		}
		responsible := false
		responsibility, err := store.RunIssueResponsibility(ctx, q, run)
		if err == nil {
			responsible = responsibility.IssueID == issue
		} else if errors.Is(err, store.ErrNotFound) {
			responsible, err = store.TaskIssueResponsible(ctx, q, r.TaskID, issue)
		}
		if err != nil {
			return out, err
		}
		if !responsible {
			return out, protocol.Errorf(protocol.CodeForbidden, "run is not controlled by this Issue")
		}
		t, err := store.GetTask(ctx, q, r.TaskID)
		if err != nil {
			return out, err
		}
		out.RunID, out.TaskID, out.HostID, out.RunObserved = run, r.TaskID, r.HostID, r.ObservedState
		out.ControlMode, out.ControlRevision = t.ControlMode, t.ControlRevision
		out.Queries = append(out.Queries, "tend task show "+handoffArgument(r.TaskID)+" --json", "tend run native-entry "+handoffArgument(run)+" --json", "tend run messages "+handoffArgument(run)+" --after 0 --json")
	}
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Restore the context of Tend Issue %s (%s) by querying Tend. This prompt does not transfer control.\nIssue revision: %d; assignment epoch: %d; context revision: %d; processed-source watermark: %d; context status: %s.\n", issue, out.Title, out.Revision, out.AssignmentEpoch, out.ContextRevision, out.Watermark, out.ContextSyncStatus)
	if out.ContextGaps != "" {
		fmt.Fprintf(&prompt, "Recorded context gaps: %s\n", out.ContextGaps)
	}
	prompt.WriteString("Use the current deployment's authenticated Tend CLI. Start with these read-only queries:\n")
	for _, query := range out.Queries {
		fmt.Fprintf(&prompt, "  %s\n", query)
	}
	prompt.WriteString("Follow context sources with --after-source <next> while more=true. Follow conversation history backward with --before <smallest-sequence> until more=false; its initial page is anchored by conversation_through. Read source originals, task/run details and authorized file metadata/downloads as referenced. A summary or one page is not complete history. Re-read the Issue/context after pagination; if revisions changed, fetch the delta before making decisions. Preserve failed and unknown results and distinguish later scope corrections from earlier requests.\n")
	prompt.WriteString("Issue context and native Run session recovery are separate. Before editing, use takeover with the current task control revision, then confirm the original automatic writer stopped on the original host/workspace. Never infer control or stop proof from a pane or session locator. Hand back explicitly only after manual writing ends; re-read control and workspace state before Tend continues. Native session recovery must use this Run's recorded environment/session, never a latest or foreign session.\n")
	if run != "" {
		fmt.Fprintf(&prompt, "Selected Run: %s; Task: %s; host: %s; task control: %s revision %d; observed Run: %s. These are snapshot facts, not permission or current stop proof.\n", run, out.TaskID, out.HostID, out.ControlMode, out.ControlRevision, out.RunObserved)
	}
	out.Prompt = prompt.String()
	return out, nil
}

func handoffArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
