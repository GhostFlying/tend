// Package views reads the ledger into the protocol's read views, for the
// main agent's tools and the owner API alike. It only reads.
package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Summary is the listed form of a task.
func Summary(r store.TaskRow) protocol.TaskSummary {
	return protocol.TaskSummary{
		TaskID: r.TaskID, Title: r.Title, Project: r.Project, Lifecycle: r.Lifecycle, ControlMode: r.ControlMode, ControlRevision: r.ControlRevision,
		SpecVersion: r.SpecVersion, Rating: r.Rating, BlockReason: r.BlockReason, Workspace: r.WorkspaceName,
		NextImplementGeneration: r.ImplementGeneration + 1,
		CompletedAt:             r.CompletedAt,
	}
}

// maxTasks bounds a task listing.
const maxTasks = 200

// Tasks lists tasks, unfinished first, leaving out finished ones unless
// asked for. A project limits the listing to its tasks; empty lists every
// project.
func Tasks(ctx context.Context, q store.Querier, includeFinished bool, project string) ([]protocol.TaskSummary, error) {
	rows, err := store.ListTasks(ctx, q, project, maxTasks)
	if err != nil {
		return nil, err
	}
	if !includeFinished {
		rows = slices.DeleteFunc(rows, func(row store.TaskRow) bool { return row.Lifecycle.Terminal() })
	}
	return SummariesWithExecution(ctx, q, rows)
}

// Task reads one task in full. A missing task is store.ErrNotFound.
func Task(ctx context.Context, q store.Querier, taskID string) (protocol.TaskDetail, error) {
	var d protocol.TaskDetail
	row, err := store.GetTask(ctx, q, taskID)
	if err != nil {
		return d, err
	}
	d.TaskSummary, err = SummaryWithExecution(ctx, q, row)
	if err != nil {
		return d, err
	}
	items, err := store.ListTaskWorkItems(ctx, q, taskID)
	if err != nil {
		return d, err
	}
	for _, item := range items {
		d.WorkItemIDs = append(d.WorkItemIDs, item.WorkItemID)
	}
	d.CurrentArtifactID = row.CurrentArtifactID
	if d.Relations, err = TaskRelations(ctx, q, taskID); err != nil {
		return d, err
	}
	spec, err := store.GetSpec(ctx, q, row.TaskID, row.SpecVersion)
	if err != nil {
		return d, err
	}
	d.Spec = json.RawMessage(spec)
	actions, err := store.ListActions(ctx, q, row.TaskID)
	if err != nil {
		return d, err
	}
	versions := make(map[string]int64, len(actions))
	for _, x := range actions {
		versions[x.ActionID] = x.TargetSpecVersion
		d.Actions = append(d.Actions, protocol.ActionView{
			ActionID: x.ActionID, Kind: x.Kind, Status: x.Status, Generation: x.Generation, SpecVersion: x.TargetSpecVersion, ArtifactID: x.TargetArtifactID,
		})
	}
	runs, err := store.ListRunsByTask(ctx, q, row.TaskID)
	if err != nil {
		return d, err
	}
	for _, r := range runs {
		d.Runs = append(d.Runs, protocol.RunView{RunID: r.RunID, Role: r.Role, Generation: r.Generation, SpecVersion: versions[r.ActionID], Host: r.HostID, Observed: r.ObservedState})
	}
	arts, err := store.ListArtifactsByTask(ctx, q, row.TaskID)
	if err != nil {
		return d, err
	}
	for _, x := range arts {
		d.Artifacts = append(d.Artifacts, protocol.ArtifactView{ArtifactID: x.ArtifactID, RunID: x.RunID, Ref: json.RawMessage(x.RefJSON)})
	}
	accs, err := store.ListAcceptances(ctx, q, row.TaskID, "")
	if err != nil {
		return d, err
	}
	d.Acceptances = Acceptances(accs, false)
	decs, err := store.ListDecisions(ctx, q, row.TaskID)
	if err != nil {
		return d, err
	}
	for _, x := range decs {
		d.Decisions = append(d.Decisions, Decision(x))
	}
	if d.WorkspaceState, err = workspace(ctx, q, row.TaskID); err != nil {
		return d, err
	}
	return d, nil
}

// workspace reads where a task's workspace was laid out, or nil if it has
// not been yet.
func workspace(ctx context.Context, q store.Querier, taskID string) (*protocol.WorkspaceView, error) {
	w, err := store.GetWorkspaceByTask(ctx, q, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	facts, err := store.ListMountFacts(ctx, q, w.WorkspaceID)
	if err != nil {
		return nil, err
	}
	v := &protocol.WorkspaceView{Host: w.HostID, Status: w.Status, Mounts: []protocol.MountView{},
		OwnerWorkItemID: w.OwnerWorkItemID, Mode: w.Mode, SourceTaskID: w.SourceTaskID, ResourceID: w.WorkspaceID, Kind: w.Kind, Name: w.Name, Path: w.Path, ProviderID: w.ProviderID, OccupiedBy: w.OccupiedBy}
	for _, path := range slices.Sorted(maps.Keys(facts)) {
		m := facts[path]
		v.Mounts = append(v.Mounts, protocol.MountView{
			Directory: m.Directory, Path: m.Path, Repo: m.Repo, Base: json.RawMessage(m.BaseJSON), ResolvedBaseSHA: m.ResolvedBaseSHA, ResolvedFrom: m.ResolvedFrom, Branch: m.Branch,
		})
	}
	return v, nil
}

// Acceptances shows verdicts, with their evidence if asked for.
func Acceptances(rows []store.AcceptanceRow, evidence bool) []protocol.AcceptanceView {
	out := make([]protocol.AcceptanceView, 0, len(rows))
	for _, a := range rows {
		v := protocol.AcceptanceView{AcceptanceID: a.AcceptanceID, ArtifactID: a.ArtifactID, SpecVersion: a.SpecVersion, Verdict: a.Verdict, Issues: json.RawMessage(a.IssuesJSON)}
		if evidence {
			v.Evidence = json.RawMessage(a.EvidenceJSON)
		}
		out = append(out, v)
	}
	return out
}

// Decision shows a decision.
func Decision(x store.DecisionRow) protocol.DecisionView {
	v := protocol.DecisionView{Worker: x.Worker, DecisionID: x.DecisionID, TaskID: x.TaskID, Question: x.Question, Options: json.RawMessage(x.OptionsJSON), Version: x.Version, Status: x.Status}
	if x.AnswerJSON != "" {
		v.Answer = json.RawMessage(x.AnswerJSON)
	}
	_ = json.Unmarshal([]byte(x.EffectsJSON), &v.Effects)
	if len(v.Effects) == 0 {
		v.Effects = nil
	}
	return v
}

// Artifact reads an artifact with every verdict on it. A missing artifact
// is store.ErrNotFound.
func Artifact(ctx context.Context, q store.Querier, artifactID string) (protocol.ArtifactDetail, error) {
	art, err := store.GetArtifact(ctx, q, artifactID)
	if err != nil {
		return protocol.ArtifactDetail{}, err
	}
	accs, err := store.ListAcceptances(ctx, q, art.TaskID, art.ArtifactID)
	if err != nil {
		return protocol.ArtifactDetail{}, err
	}
	return protocol.ArtifactDetail{
		Artifact: protocol.ArtifactView{ArtifactID: art.ArtifactID, RunID: art.RunID, Ref: json.RawMessage(art.RefJSON)},
		TaskID:   art.TaskID, Acceptances: Acceptances(accs, true),
	}, nil
}

// Request reads the recorded outcome of a request in a dedup domain. A
// request never recorded is store.ErrNotFound.
func Request(ctx context.Context, q store.Querier, domain, requestID string) (protocol.RequestView, error) {
	rec, err := store.GetRequest(ctx, q, domain, requestID)
	if err != nil {
		return protocol.RequestView{}, err
	}
	v := protocol.RequestView{RequestID: rec.RequestID, Operation: rec.Operation, Status: rec.Status}
	if rec.ResultJSON != "" {
		v.Result = json.RawMessage(rec.ResultJSON)
	}
	if rec.ErrorCode != "" {
		v.Error = &protocol.Error{Code: protocol.Code(rec.ErrorCode), Message: rec.ErrorMessage}
		if rec.ErrorDetails != "" {
			v.Error.Details = json.RawMessage(rec.ErrorDetails)
		}
	}
	return v, nil
}

// Snapshot reads the owner's overview, of one project or, when project is
// empty, of all. queue is the whole queue in claim order; the projects'
// loads are counted over all of it.
func Snapshot(ctx context.Context, q store.Querier, queue []protocol.QueueEntry, project string) (protocol.Snapshot, error) {
	var v protocol.Snapshot
	var err error
	if project != "" {
		if _, err := store.GetProject(ctx, q, project); err != nil {
			return v, err
		}
	}
	if v.EventSeq, err = store.LatestEventSeq(ctx, q); err != nil {
		return v, err
	}
	latestUpgrade, upgradeErr := store.LatestUpgrade(ctx, q)
	if upgradeErr == nil {
		v.Upgrade = &latestUpgrade.View
	} else if !errors.Is(upgradeErr, store.ErrNotFound) {
		return v, upgradeErr
	}
	// Scoped before the cap on the number of tasks.
	if v.Tasks, err = Tasks(ctx, q, false, project); err != nil {
		return v, err
	}
	decs, err := store.ListDecisions(ctx, q, "")
	if err != nil {
		return v, err
	}
	v.Decisions = make([]protocol.DecisionView, 0, len(decs))
	projectOf := map[string]string{}
	asking := map[string]bool{} // projects with open decisions
	for _, x := range decs {
		d := Decision(x)
		if x.TaskID != "" {
			p, ok := projectOf[x.TaskID]
			if !ok {
				t, err := store.GetTask(ctx, q, x.TaskID)
				if err != nil {
					return v, err
				}
				p, projectOf[x.TaskID] = t.Project, t.Project
			}
			d.Project = p
			asking[p] = true
		}
		// A decision about no task concerns every project.
		if project == "" || d.Project == "" || d.Project == project {
			v.Decisions = append(v.Decisions, d)
		}
	}
	v.Hosts, err = Hosts(ctx, q)
	if err != nil {
		return v, err
	}
	if v.Tasks == nil {
		v.Tasks = []protocol.TaskSummary{}
	}
	if v.Projects, err = projects(ctx, q, queue, asking, project); err != nil {
		return v, err
	}
	v.Queue = []protocol.QueueEntry{}
	for _, e := range queue {
		if project == "" || e.Project == project {
			v.Queue = append(v.Queue, e)
		}
	}
	if project != "" {
		v.Projects = slices.DeleteFunc(v.Projects, func(p protocol.ProjectView) bool { return p.Project != project })
	}
	return v, nil
}

// projects lists the active projects, the retired ones that still have
// unfinished tasks, open runs or, as asking says, open decisions, and the
// project asked for.
func projects(ctx context.Context, q store.Querier, queue []protocol.QueueEntry, asking map[string]bool, asked string) ([]protocol.ProjectView, error) {
	rows, err := store.ListProjects(ctx, q)
	if err != nil {
		return nil, err
	}
	runs, err := store.CountOpenRunsByProject(ctx, q)
	if err != nil {
		return nil, err
	}
	// Counted in full: the snapshot's task list is capped.
	open, err := store.CountOpenTasks(ctx, q)
	if err != nil {
		return nil, err
	}
	queued := map[string]int64{}
	for _, e := range queue {
		queued[e.Project]++
	}
	out := []protocol.ProjectView{}
	for _, p := range rows {
		if p.Retired && open[p.Project] == 0 && runs[p.Project] == 0 && !asking[p.Project] && p.Project != asked {
			continue
		}
		out = append(out, protocol.ProjectView{
			Project: p.Project, Title: p.Title, Weight: p.Weight, Retired: p.Retired,
			OpenTasks: int64(open[p.Project]), OpenRuns: runs[p.Project], Queued: queued[p.Project],
		})
	}
	return out, nil
}

// RunLogs reads up to limit of a run's logs after seq. A missing run is
// store.ErrNotFound.
func RunLogs(ctx context.Context, q store.Querier, runID string, after int64, limit int) (protocol.RunLogPage, error) {
	run, err := store.GetRun(ctx, q, runID)
	if err != nil {
		return protocol.RunLogPage{}, err
	}
	rows, err := store.ListRunLogs(ctx, q, runID, after, limit)
	if err != nil {
		return protocol.RunLogPage{}, err
	}
	page := protocol.RunLogPage{RunID: runID, Observed: run.ObservedState, Logs: make([]protocol.RunLogView, 0, len(rows)), Next: after}
	for _, l := range rows {
		page.Logs = append(page.Logs, protocol.RunLogView{
			Seq: l.Seq, NativeID: l.NativeID, Kind: l.Kind, Body: json.RawMessage(l.BodyJSON), CreatedAt: time.UnixMilli(l.CreatedAt).UTC(),
		})
		page.Next = l.Seq
	}
	return page, nil
}

// Conversation reads up to limit conversation items: after seq after when
// after is positive, else before seq before when before is positive, else
// the latest (D69).
func Conversation(ctx context.Context, q store.Querier, before, after int64, limit int, session ...string) (protocol.ConversationPage, error) {
	rows, err := store.ListConversation(ctx, q, before, after, limit+1, session...)
	if err != nil {
		return protocol.ConversationPage{}, err
	}
	page := protocol.ConversationPage{Items: make([]protocol.ConversationItem, 0, min(len(rows), limit))}
	if len(rows) > limit {
		page.More = true
		if after > 0 {
			rows = rows[:limit]
		} else {
			rows = rows[1:]
		}
	}
	for _, r := range rows {
		item, err := conversationItem(ctx, q, r)
		if err != nil {
			return protocol.ConversationPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

// conversationItem shows a conversation row to the owner.
func conversationItem(ctx context.Context, q store.Querier, r store.ConversationRow) (protocol.ConversationItem, error) {
	it := protocol.ConversationItem{ConversationID: r.ConversationID, Seq: r.Seq, Kind: r.Kind, MessageID: r.MessageID, Author: r.Author, Body: r.Body,
		ReplyTo: r.ReplyTo, CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
		AssistantState: protocol.AssistantState(r.AssistantState), AssistantRevision: r.AssistantRevision}
	associations, err := store.MessageAssociations(ctx, q, r.MessageID)
	if err != nil {
		return it, err
	}
	it.Associations = &associations
	var link protocol.ConversationReturnLink
	if err := q.QueryRowContext(ctx, `SELECT conversation_id,source_message_id FROM conversation_return_links WHERE message_id=? OR message_id=? ORDER BY CASE WHEN message_id=? THEN 0 ELSE 1 END LIMIT 1`, r.MessageID, r.ReplyTo, r.MessageID).Scan(&link.ConversationID, &link.MessageID); err == nil {
		it.ReturnLink = &link
	} else if err != sql.ErrNoRows {
		return it, err
	}
	switch {
	case r.Tool != nil:
		it.Tool = &protocol.ToolUseView{Name: r.Tool.Tool, Input: r.Tool.Input, Output: r.Tool.Output,
			Status: protocol.ToolStatus(r.Tool.Status), TurnID: r.Tool.TurnID, UpdatedAt: time.UnixMilli(r.Tool.UpdatedAt).UTC()}
	case r.Author == "owner" && r.State == "":
		it.State = protocol.MessageQueued
	case r.Author == "owner":
		it.State = protocol.MessageState(r.State)
	case r.Author == "system" && r.State != "":
		// Only notices that initiated an event turn have a recorded state.
		// Other system notices remain facts without a queued/processing state.
		it.State = protocol.MessageState(r.State)
	}
	if r.Author == "system" {
		batch, err := store.BackgroundByMessage(ctx, q, r.MessageID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return it, err
		}
		if err == nil {
			it.Background = &batch
		}
	}
	if r.Author == "owner" {
		c, err := store.MessageControl(ctx, q, r.MessageID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return it, err
		}
		if err == nil {
			it.Control = &c
		}
	}
	if r.DecisionJSON != "" {
		var c protocol.DecisionChoice
		if json.Unmarshal([]byte(r.DecisionJSON), &c) == nil {
			it.Decision = &c
		}
	}
	images, err := store.ConversationImages(ctx, q, r.MessageID)
	if err != nil {
		return protocol.ConversationItem{}, err
	}
	it.Images = images
	it.Attachments, err = store.ConversationAttachments(ctx, q, r.MessageID)
	if err != nil {
		return protocol.ConversationItem{}, err
	}
	choices, err := store.ListWorkItemChoices(ctx, q, "", r.MessageID)
	if err != nil {
		return it, err
	}
	it.Choices = choices
	notice, err := store.GetMessageNotice(ctx, q, r.MessageID)
	if err == nil {
		it.NoticeType = notice.NoticeType
		it.UpgradeID = notice.UpgradeID
		it.HostID = notice.HostID
	} else if !errors.Is(err, store.ErrNotFound) {
		return protocol.ConversationItem{}, err
	}
	return it, nil
}

// Events reads up to limit ledger events after seq for the owner's event
// stream, with the current conversation item of conversation events.
func Events(ctx context.Context, q store.Querier, after int64, limit int) ([]protocol.LedgerEvent, error) {
	rows, err := store.ListEvents(ctx, q, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.LedgerEvent, 0, len(rows))
	for _, e := range rows {
		ev := protocol.LedgerEvent{EventSeq: e.Seq, ObjectType: e.ObjectType, ObjectID: e.ObjectID, Kind: e.Kind, CreatedAt: time.UnixMilli(e.CreatedAt).UTC()}
		if e.ObjectType == "message" {
			r, err := store.GetConversationItem(ctx, q, e.ObjectID)
			switch {
			case err == nil:
				it, err := conversationItem(ctx, q, r)
				if err != nil {
					return nil, err
				}
				ev.Item = &it
			case !errors.Is(err, store.ErrNotFound):
				return nil, err
			}
		}
		out = append(out, ev)
	}
	return out, nil
}

// TaskRelations reads declarations and backlinks without recursively expanding them.
func TaskRelations(ctx context.Context, q store.Querier, taskID string) ([]protocol.TaskRelationView, error) {
	rows, err := store.ListTaskRelations(ctx, q, taskID)
	if err != nil {
		return nil, err
	}
	var out []protocol.TaskRelationView
	related := make([]store.TaskRow, 0, len(rows))
	for _, r := range rows {
		other, direction := r.TargetTaskID, protocol.RelationOutgoing
		if r.TargetTaskID == taskID {
			other, direction = r.SourceTaskID, protocol.RelationIncoming
		}
		t, err := store.GetTask(ctx, q, other)
		if err != nil {
			return nil, err
		}
		related = append(related, t)
		out = append(out, protocol.TaskRelationView{Kind: r.Kind, Direction: direction})
	}
	summaries, err := SummariesWithExecution(ctx, q, related)
	if err != nil {
		return nil, err
	}
	for i, summary := range summaries {
		out[i].Task = summary
	}
	return out, nil
}

// Hosts returns registered execution hosts with capacity and runtime state.
func Hosts(ctx context.Context, q store.Querier) ([]protocol.HostView, error) {
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return nil, err
	}
	result := make([]protocol.HostView, 0, len(hosts))
	for _, h := range hosts {
		n, err := store.CountOpenRuns(ctx, q, h.HostID)
		if err != nil {
			return nil, err
		}
		hv := protocol.HostView{HostID: h.HostID, OS: h.OS, Arch: h.Arch, Labels: h.Labels, Slots: h.Slots, Worker: h.Worker, WorkspaceProtocol: h.WorkspaceProtocol, OpenRuns: n}
		rt, err := store.GetHostRuntime(ctx, q, h.HostID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			stamp := time.UnixMilli(rt.LastHeartbeatAt)
			hv.Online = &rt.Online
			hv.LastHeartbeatAt = &stamp
			hv.BuildVersion = rt.Runtime.Build.Version
			hv.ProtocolVersion = &rt.Runtime.Protocol
		}
		control, err := store.GetHostControl(ctx, q, h.HostID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if control.Mode == protocol.RunnerMaintenance || rt.Runtime.Mode == protocol.RunnerMaintenance || rt.Runtime.Mode == protocol.RunnerReady {
			hv.Maintenance = &protocol.HostMaintenanceView{Mode: protocol.RunnerMaintenance, Phase: "draining", ReleaseID: control.ReleaseID, UpgradeID: control.UpgradeID, RemainingRuns: n}
			if rt.Runtime.Mode == protocol.RunnerReady && n == 0 && rt.Runtime.AppliedControlRevision == control.Revision {
				hv.Maintenance.Mode = protocol.RunnerReady
				hv.Maintenance.Phase = "ready"
			}
			if len(rt.Runtime.Blockers) > 0 {
				hv.Maintenance.Reason = strings.Join(rt.Runtime.Blockers, ", ")
			}
		}
		result = append(result, hv)
	}
	return result, nil
}
