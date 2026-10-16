// Package views reads the ledger into the protocol's read views, for the
// main agent's tools and the owner API alike. It only reads.
package views

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Summary is the listed form of a task.
func Summary(r store.TaskRow) protocol.TaskSummary {
	return protocol.TaskSummary{
		TaskID: r.TaskID, Title: r.Title, Lifecycle: r.Lifecycle, ControlMode: r.ControlMode, ControlRevision: r.ControlRevision,
		SpecVersion: r.SpecVersion, Priority: r.Priority, BlockReason: r.BlockReason, Workspace: r.WorkspaceName,
		NextImplementGeneration: r.ImplementGeneration + 1,
	}
}

// maxTasks bounds a task listing.
const maxTasks = 200

// Tasks lists tasks, unfinished first, leaving out finished ones unless
// asked for.
func Tasks(ctx context.Context, q store.Querier, includeFinished bool) ([]protocol.TaskSummary, error) {
	rows, err := store.ListTasks(ctx, q, maxTasks)
	if err != nil {
		return nil, err
	}
	var out []protocol.TaskSummary
	for _, r := range rows {
		if includeFinished || !r.Lifecycle.Terminal() {
			out = append(out, Summary(r))
		}
	}
	return out, nil
}

// Task reads one task in full. A missing task is store.ErrNotFound.
func Task(ctx context.Context, q store.Querier, taskID string) (protocol.TaskDetail, error) {
	var d protocol.TaskDetail
	row, err := store.GetTask(ctx, q, taskID)
	if err != nil {
		return d, err
	}
	d.TaskSummary = Summary(row)
	spec, err := store.GetSpec(ctx, q, row.TaskID, row.SpecVersion)
	if err != nil {
		return d, err
	}
	d.Spec = json.RawMessage(spec)
	actions, err := store.ListActions(ctx, q, row.TaskID)
	if err != nil {
		return d, err
	}
	for _, x := range actions {
		d.Actions = append(d.Actions, protocol.ActionView{
			ActionID: x.ActionID, Kind: x.Kind, Status: x.Status, Generation: x.Generation, SpecVersion: x.TargetSpecVersion, ArtifactID: x.TargetArtifactID,
		})
	}
	runs, err := store.ListRunsByTask(ctx, q, row.TaskID)
	if err != nil {
		return d, err
	}
	for _, r := range runs {
		d.Runs = append(d.Runs, protocol.RunView{RunID: r.RunID, Role: r.Role, Generation: r.Generation, Host: r.HostID, Observed: r.ObservedState})
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
	v := &protocol.WorkspaceView{Host: w.HostID, Status: w.Status, Mounts: []protocol.MountView{}}
	for _, path := range slices.Sorted(maps.Keys(facts)) {
		m := facts[path]
		v.Mounts = append(v.Mounts, protocol.MountView{
			Path: m.Path, Repo: m.Repo, Base: json.RawMessage(m.BaseJSON), ResolvedBaseSHA: m.ResolvedBaseSHA, ResolvedFrom: m.ResolvedFrom, Branch: m.Branch,
		})
	}
	return v, nil
}

// Acceptances shows verdicts, with their evidence if asked for.
func Acceptances(rows []store.AcceptanceRow, evidence bool) []protocol.AcceptanceView {
	out := make([]protocol.AcceptanceView, 0, len(rows))
	for _, a := range rows {
		v := protocol.AcceptanceView{ArtifactID: a.ArtifactID, SpecVersion: a.SpecVersion, Verdict: a.Verdict, Issues: json.RawMessage(a.IssuesJSON)}
		if evidence {
			v.Evidence = json.RawMessage(a.EvidenceJSON)
		}
		out = append(out, v)
	}
	return out
}

// Decision shows a decision.
func Decision(x store.DecisionRow) protocol.DecisionView {
	v := protocol.DecisionView{DecisionID: x.DecisionID, TaskID: x.TaskID, Question: x.Question, Options: json.RawMessage(x.OptionsJSON), Version: x.Version, Status: x.Status}
	if x.AnswerJSON != "" {
		v.Answer = json.RawMessage(x.AnswerJSON)
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

// Snapshot reads the owner's overview. q should be a single read
// transaction, so the event sequence matches what is shown.
func Snapshot(ctx context.Context, q store.Querier) (protocol.Snapshot, error) {
	var v protocol.Snapshot
	var err error
	if v.EventSeq, err = store.LatestEventSeq(ctx, q); err != nil {
		return v, err
	}
	if v.Tasks, err = Tasks(ctx, q, false); err != nil {
		return v, err
	}
	decs, err := store.ListDecisions(ctx, q, "")
	if err != nil {
		return v, err
	}
	v.Decisions = make([]protocol.DecisionView, 0, len(decs))
	for _, x := range decs {
		v.Decisions = append(v.Decisions, Decision(x))
	}
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return v, err
	}
	v.Hosts = make([]protocol.HostView, 0, len(hosts))
	for _, h := range hosts {
		n, err := store.CountOpenRuns(ctx, q, h.HostID)
		if err != nil {
			return v, err
		}
		v.Hosts = append(v.Hosts, protocol.HostView{HostID: h.HostID, OS: h.OS, Arch: h.Arch, Labels: h.Labels, Slots: h.Slots, OpenRuns: n})
	}
	if v.Tasks == nil {
		v.Tasks = []protocol.TaskSummary{}
	}
	return v, nil
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
