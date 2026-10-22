package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// DefaultLeaseTTL is how long a run lease lasts without renewal.
const DefaultLeaseTTL = time.Minute

// WithLeaseTTL sets the lease duration of claimed runs.
func WithLeaseTTL(d time.Duration) Option {
	return func(a *App) { a.leaseTTL = d }
}

// DefaultWaitPerHour is what an hour in the queue adds to an action's
// score: with weight 1, ten hours make up one rating step (D60).
const DefaultWaitPerHour = 0.1

// WithWaitPerHour sets what an hour in the queue adds to an action's
// score.
func WithWaitPerHour(w float64) Option {
	return func(a *App) { a.waitPer = w }
}

// LeaseTTL returns the lease duration of claimed runs.
func (a *App) LeaseTTL() time.Duration { return a.leaseTTL }

// Changes is notified after every committed change to the ledger, for
// example so that waiting runner claims look for work again.
func (a *App) Changes() *Broadcast { return a.changes }

func (a *App) notifyChange() { a.changes.Notify() }

// RunLogs is notified after run logs are recorded. Logs are not events,
// so Changes does not cover them.
func (a *App) RunLogs() *Broadcast { return a.logs }

// requireActiveHost returns the calling runner's host.
func requireActiveHost(ctx context.Context, x *Exec, q store.Querier) (store.HostRow, error) {
	if err := allowKinds(x.Principal, protocol.PrincipalRunner); err != nil {
		return store.HostRow{}, err
	}
	h, err := store.GetHost(ctx, q, x.Principal.Subject)
	if errors.Is(err, store.ErrNotFound) {
		return store.HostRow{}, protocol.Errorf(protocol.CodeForbidden, "host %s is not configured", x.Principal.Subject)
	}
	if err != nil {
		return store.HostRow{}, err
	}
	if h.Status != store.HostActive {
		return store.HostRow{}, protocol.Errorf(protocol.CodeForbidden, "host %s is retired", h.HostID)
	}
	return h, nil
}

// checkWorker refuses a runner whose harness is not the one the server's
// configuration names for its host (D64): tend runs neither side's choice
// in place of the other. It runs in Authorize, which also guards a resent
// request answered from its record, so a claim replayed after the host's
// worker changed hands out nothing.
func checkWorker(ctx context.Context, x *Exec, q store.Querier) error {
	h, err := requireActiveHost(ctx, x, q)
	if err != nil {
		return err
	}
	var p struct {
		Worker protocol.WorkerKind `json:"worker"`
	}
	if len(x.Env.Payload) > 0 {
		// The payload is validated in full by Apply.
		_ = json.Unmarshal(x.Env.Payload, &p)
	}
	if got := cmp.Or(p.Worker, protocol.WorkerAppServer); got != h.Worker {
		return protocol.Errorf(protocol.CodeInvalidRequest,
			"host %s is configured for the %s worker, but its runner runs %s", h.HostID, h.Worker, got)
	}
	return nil
}

// desiredFor derives what tend wants a run to do from the current ledger:
// work that no longer matches the task stops, a task under human control
// holds its runs, and a paused task pauses them.
func desiredFor(run store.RunRow, action store.ActionRow, task store.TaskRow) protocol.RunDesired {
	switch {
	case run.ObservedState == protocol.RunFinished || run.ObservedState == protocol.RunFailed,
		task.Lifecycle.Terminal(),
		action.Status == protocol.ActionCancelled || action.Status == protocol.ActionSuperseded,
		action.TargetSpecVersion != task.SpecVersion:
		return protocol.DesiredStopped
	case task.ControlMode == protocol.ControlHumanControl:
		return protocol.DesiredHeld
	case task.ControlMode != protocol.ControlManaged:
		return protocol.DesiredPaused
	}
	return protocol.DesiredRun
}

func roleOf(kind protocol.ActionKind) protocol.RunRole {
	if kind == protocol.ActionAccept {
		return protocol.RoleAccept
	}
	return protocol.RoleImplement
}

// buildAssignment describes a run to the runner that will execute it.
func buildAssignment(ctx context.Context, q store.Querier, run store.RunRow, lease store.LeaseRow, ttl time.Duration) (protocol.Assignment, error) {
	action, err := store.GetAction(ctx, q, run.ActionID)
	if err != nil {
		return protocol.Assignment{}, err
	}
	task, err := store.GetTask(ctx, q, run.TaskID)
	if err != nil {
		return protocol.Assignment{}, err
	}
	specJSON, err := store.GetSpec(ctx, q, run.TaskID, action.TargetSpecVersion)
	if err != nil {
		return protocol.Assignment{}, err
	}
	var spec domain.Spec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return protocol.Assignment{}, fmt.Errorf("app: decode spec of %s: %w", run.TaskID, err)
	}
	ws := protocol.AssignmentWorkspace{
		Name: task.WorkspaceName, Branch: domain.TaskBranch(task.WorkspaceName), Profile: spec.Workspace.Profile,
	}
	resource, err := store.GetWorkspaceByTask(ctx, q, run.TaskID)
	if err != nil {
		return protocol.Assignment{}, err
	}
	ws.ID, ws.Kind, ws.Reused, ws.TaskTitle = resource.WorkspaceID, resource.Kind, resource.Mode == domain.WorkspaceReuse, task.Title
	if ws.ShortTaskID, err = shortTaskID(ctx, q, run.TaskID); err != nil {
		return protocol.Assignment{}, err
	}
	ws.Name, ws.Branch = resource.Name, domain.TaskBranch(resource.Name)
	ws.ProviderID, ws.ProviderInstanceID, ws.Path = resource.ProviderID, resource.ProviderInstanceID, resource.Path
	if baseline, e := store.WorkspaceBaseline(ctx, q, resource.WorkspaceID, run.TaskID); e == nil {
		ws.Baseline = json.RawMessage(baseline)
	} else if !errors.Is(e, store.ErrNotFound) {
		return protocol.Assignment{}, e
	}
	for _, m := range spec.Workspace.Mounts {
		repo, err := store.GetRepo(ctx, q, m.Repo)
		if err != nil {
			return protocol.Assignment{}, err
		}
		ws.Mounts = append(ws.Mounts, protocol.AssignedMount{
			Repo: m.Repo, Path: m.Path, RemoteURL: repo.RemoteURL, BaseBranch: m.Base.Branch, BaseCommit: m.Base.Commit,
			BaseCandidates: repo.BaseCandidates, Profile: m.Profile,
		})
	}
	relations, err := views.TaskRelations(ctx, q, task.TaskID)
	if err != nil {
		return protocol.Assignment{}, err
	}
	as := protocol.Assignment{
		Relations: relations,
		RunID:     run.RunID, LeaseID: lease.LeaseID, LeaseTTLMillis: ttl.Milliseconds(), TaskID: run.TaskID,
		ActionID: run.ActionID, Role: run.Role, Desired: desiredFor(run, action, task), Generation: run.Generation,
		SpecVersion: action.TargetSpecVersion,
		Spec:        json.RawMessage(specJSON), Workspace: ws,
	}
	if action.Kind == protocol.ActionAccept {
		ref, err := store.GetArtifactRef(ctx, q, action.TargetArtifactID)
		if err != nil {
			return protocol.Assignment{}, err
		}
		as.Artifact = json.RawMessage(ref)
	}
	if action.Kind == protocol.ActionImplement && action.Generation > 1 {
		// A later generation should know what the last acceptance of this
		// spec version asked to change.
		accs, err := store.ListAcceptances(ctx, q, run.TaskID, "")
		if err != nil {
			return protocol.Assignment{}, err
		}
		for i := len(accs) - 1; i >= 0; i-- {
			if a := accs[i]; a.SpecVersion == action.TargetSpecVersion && a.Verdict == protocol.VerdictChangesRequired {
				as.Feedback = &protocol.Feedback{Verdict: a.Verdict, Issues: json.RawMessage(a.IssuesJSON), Evidence: json.RawMessage(a.EvidenceJSON)}
				break
			}
		}
	}
	return as, nil
}

// claimResult is the recorded result of a claim.
type claimResult struct {
	Assignment protocol.Assignment `json:"assignment"`
}

// Claim handles runner.claim: the runner asks for the next run it should
// execute. The claim evaluates the scheduling rules inside its transaction
// and allocates the run, its lease and, for a task's first implement run,
// the task's workspace on this host. Finding no work has no effect, so an
// idle poll leaves no request record.
type Claim struct{}

// Operation implements Command.
func (Claim) Operation() string { return protocol.OpRunnerClaim }

// Authorize implements Command.
func (Claim) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	return checkWorker(ctx, x, q)
}

// Apply implements Command.
func (Claim) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ClaimPayload
	if len(x.Env.Payload) > 0 {
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
	}
	q := tx.Q()
	host, err := requireActiveHost(ctx, x, q)
	if err != nil {
		return Outcome{}, err
	}
	if p.WorkspaceProtocol < 0 || p.WorkspaceProtocol > 1 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unsupported workspace_protocol")
	}
	if _, err := tx.Exec(ctx, `UPDATE hosts SET workspace_protocol=? WHERE host_id=?`, p.WorkspaceProtocol, host.HostID); err != nil {
		return Outcome{}, fmt.Errorf("app: report claim capabilities: %w", err)
	}
	if host.WorkspaceProtocol != p.WorkspaceProtocol {
		if err := tx.InvalidateBaselines(ctx, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	busy, err := store.CountOpenRuns(ctx, q, host.HostID)
	if err != nil {
		return Outcome{}, err
	}
	rows, err := store.ListClaimCandidates(ctx, q)
	if err != nil {
		return Outcome{}, err
	}
	candidates, err := candidatesOf(rows)
	if err != nil {
		return Outcome{}, err
	}
	pick, ok := scheduler.Pick(scheduler.Host{
		ID: host.HostID, OS: host.OS, Active: true, Labels: host.Labels, Repos: host.Repos, Slots: host.Slots, Busy: busy,
		Worker: host.Worker.Capabilities(), WorkspaceProtocol: p.WorkspaceProtocol,
	}, candidates, scheduler.Order{Now: x.Now.UnixMilli(), WaitPerHour: x.WaitPerHour})
	if !ok {
		return Outcome{Result: map[string]any{"assignment": nil}, NoEffect: true}, nil
	}

	now := x.Now.UnixMilli()
	run := store.RunRow{
		RunID: x.IDs.New(protocol.PrefixRun), TaskID: pick.TaskID, ActionID: pick.ActionID, Generation: pick.Generation,
		Role: roleOf(pick.Kind), HostID: host.HostID, DesiredState: protocol.DesiredRun,
		ObservedState: protocol.RunStarting, CreatedAt: now,
	}
	if err := tx.InsertRun(ctx, run); err != nil {
		return Outcome{}, err
	}
	lease := store.LeaseRow{LeaseID: x.IDs.New(protocol.PrefixLease), RunID: run.RunID, HostID: host.HostID, ExpiresAt: x.Now.Add(x.LeaseTTL).UnixMilli()}
	if err := tx.InsertLease(ctx, lease, now); err != nil {
		return Outcome{}, err
	}
	if err := tx.SetActionStatus(ctx, pick.ActionID, protocol.ActionDispatched, now); err != nil {
		return Outcome{}, err
	}
	if pick.Kind == protocol.ActionImplement && pick.WorkspaceHost == "" {
		task, err := store.GetTask(ctx, q, pick.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		if err := tx.InsertWorkspace(ctx, store.WorkspaceRow{
			WorkspaceID: x.IDs.New(protocol.PrefixWorkspace), TaskID: pick.TaskID, Name: task.WorkspaceName,
			HostID: host.HostID, Status: store.WorkspaceAssigned, Kind: workspaceKind(pick), Mode: workspaceMode(pick),
		}, now); err != nil {
			return Outcome{}, err
		}
	}
	resource, err := store.GetWorkspaceByTask(ctx, q, run.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	if err = tx.AcquireWorkspace(ctx, resource.WorkspaceID, run.TaskID, now); err != nil {
		return Outcome{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workspace_runs(run_id,workspace_id) VALUES(?,?)`, run.RunID, resource.WorkspaceID); err != nil {
		return Outcome{}, fmt.Errorf("app: bind workspace run: %w", err)
	}
	as, err := buildAssignment(ctx, q, run, lease, x.LeaseTTL)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Result: claimResult{Assignment: as},
		Events: []Event{{ObjectType: "run", ObjectID: run.RunID, Kind: "run.assigned", Body: map[string]any{
			"task_id": run.TaskID, "action_id": run.ActionID, "role": run.Role, "host_id": host.HostID,
		}}},
	}, nil
}

// Hello handles runner.hello: the runner reports the runs its journal holds
// before claiming anything, and tend reconciles them with the ledger.
type Hello struct{}

// Operation implements Command.
func (Hello) Operation() string { return protocol.OpRunnerHello }

// Authorize implements Command.
func (Hello) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	return checkWorker(ctx, x, q)
}

// Apply implements Command.
func (Hello) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.HelloPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	q := tx.Q()
	host, err := requireActiveHost(ctx, x, q)
	if err != nil {
		return Outcome{}, err
	}
	if p.WorkspaceProtocol < 0 || p.WorkspaceProtocol > 1 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unsupported workspace_protocol")
	}
	if _, err := tx.Exec(ctx, `UPDATE hosts SET workspace_protocol=? WHERE host_id=?`, p.WorkspaceProtocol, host.HostID); err != nil {
		return Outcome{}, fmt.Errorf("app: report runner capabilities: %w", err)
	}
	now := x.Now.UnixMilli()
	expires := x.Now.Add(x.LeaseTTL).UnixMilli()
	if err := tx.TouchHost(ctx, host.HostID, now); err != nil {
		return Outcome{}, err
	}
	res := protocol.HelloResult{LeaseTTLMillis: x.LeaseTTL.Milliseconds(), Runs: []protocol.RunVerdict{}, Reassigned: []protocol.Assignment{}}
	var events []Event
	if host.WorkspaceProtocol != p.WorkspaceProtocol {
		if err := tx.InvalidateBaselines(ctx, now); err != nil {
			return Outcome{}, err
		}
		events = append(events, Event{ObjectType: "host", ObjectID: host.HostID, Kind: "host.worker_capabilities_changed", Body: map[string]any{"workspace_protocol": p.WorkspaceProtocol}})
	}
	held := map[string]bool{}
	stop := func(runID, reason string) {
		res.Runs = append(res.Runs, protocol.RunVerdict{RunID: runID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped, Reason: reason})
	}
	for _, h := range p.Held {
		held[h.RunID] = true
		run, err := store.GetRun(ctx, q, h.RunID)
		if errors.Is(err, store.ErrNotFound) {
			// For example after the ledger was restored from an older
			// backup: never replay, ask the runner to stop.
			stop(h.RunID, "tend has no record of this run")
			events = append(events, Event{ObjectType: "host", ObjectID: host.HostID, Kind: "runner.unknown_run", Body: map[string]any{"run_id": h.RunID}})
			continue
		}
		if err != nil {
			return Outcome{}, err
		}
		if run.HostID != host.HostID {
			stop(h.RunID, "the run belongs to another host")
			continue
		}
		lease, err := store.GetLeaseByRun(ctx, q, run.RunID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return Outcome{}, err
		}
		if err != nil || lease.LeaseID != h.LeaseID || lease.Status == store.LeaseReleased {
			stop(h.RunID, "the runner's lease is not the run's current lease")
			continue
		}
		rc, err := loadRun(ctx, q, run.RunID)
		if err != nil {
			return Outcome{}, err
		}
		desired := desiredFor(run, rc.action, rc.task)
		if desired == protocol.DesiredStopped {
			stop(h.RunID, "the run is no longer wanted")
			continue
		}
		if err := tx.SetLease(ctx, lease.LeaseID, lease.Status, store.LeaseActive, expires, now); err != nil {
			return Outcome{}, err
		}
		// Only a live state is taken over from the runner. A run that
		// says it finished stays unknown until its result arrives, so the
		// result can still be delivered.
		if run.ObservedState == protocol.RunUnknown && slices.Contains(reconcilable, h.Observed) {
			if err := tx.UpdateRunObserved(ctx, run.RunID, h.Observed, now); err != nil {
				return Outcome{}, err
			}
			events = append(events, Event{ObjectType: "run", ObjectID: run.RunID, Kind: "run.reconciled", Body: map[string]any{"observed": h.Observed}})
		}
		res.Runs = append(res.Runs, protocol.RunVerdict{RunID: run.RunID, Verdict: protocol.VerdictContinue, Desired: desired})
	}

	open, err := store.ListOpenRunsOnHost(ctx, q, host.HostID)
	if err != nil {
		return Outcome{}, err
	}
	for _, run := range open {
		if held[run.RunID] {
			continue
		}
		lease, err := store.GetLeaseByRun(ctx, q, run.RunID)
		if err != nil {
			return Outcome{}, err
		}
		if run.ObservedState == protocol.RunStarting {
			// The runner journals a run before starting it, so a run it
			// does not hold never started. Hand the same run over again
			// instead of allocating a second one, unless the work is no
			// longer wanted.
			rc, err := loadRun(ctx, q, run.RunID)
			if err != nil {
				return Outcome{}, err
			}
			if desiredFor(run, rc.action, rc.task) == protocol.DesiredStopped {
				if err := endRun(ctx, x, tx, run, protocol.RunFailed); err != nil {
					return Outcome{}, err
				}
				events = append(events, Event{ObjectType: "run", ObjectID: run.RunID, Kind: "run.abandoned", Body: map[string]any{"reason": "the run never started and is no longer wanted"}})
				continue
			}
			if err := tx.SetLease(ctx, lease.LeaseID, lease.Status, store.LeaseActive, expires, now); err != nil {
				return Outcome{}, err
			}
			lease.ExpiresAt = expires
			as, err := buildAssignment(ctx, q, run, lease, x.LeaseTTL)
			if err != nil {
				return Outcome{}, err
			}
			res.Reassigned = append(res.Reassigned, as)
			events = append(events, Event{ObjectType: "run", ObjectID: run.RunID, Kind: "run.reassigned", Body: map[string]any{"host_id": host.HostID}})
			continue
		}
		// The run started, but the runner no longer knows it, for example
		// because its journal was lost. Whether anything still writes to
		// the workspace cannot be established.
		if run.ObservedState != protocol.RunUnknown {
			if err := tx.UpdateRunObserved(ctx, run.RunID, protocol.RunUnknown, now); err != nil {
				return Outcome{}, err
			}
		}
		if lease.Status == store.LeaseActive {
			if err := tx.SetLease(ctx, lease.LeaseID, store.LeaseActive, store.LeaseLapsed, lease.ExpiresAt, now); err != nil {
				return Outcome{}, err
			}
		}
		events = append(events, Event{ObjectType: "run", ObjectID: run.RunID, Kind: "run.lost", Body: map[string]any{"host_id": host.HostID}})
	}
	return Outcome{Result: res, Events: events}, nil
}

// ReportWorkspace handles runs.report_workspace: before starting the
// worker, an implement run records how each mount of the task's workspace
// was laid out. A mount is recorded once; later runs of the task report the
// same facts again for mounts that already exist.
type ReportWorkspace struct{}

// Operation implements Command.
func (ReportWorkspace) Operation() string { return protocol.OpRunsReportWorkspace }

// Authorize implements Command.
func (ReportWorkspace) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return authorizeRunSelf(x, protocol.RoleImplement)
}

// reconcilable are the states hello takes over from a runner for a run
// tend lost track of.
var reconcilable = []protocol.RunObserved{protocol.RunStarting, protocol.RunRunning, protocol.RunWaiting, protocol.RunInterrupted}

var shaPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Apply implements Command.
func (ReportWorkspace) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.WorkspaceReport
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	q := tx.Q()
	rc, err := loadRun(ctx, q, x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	spec, err := runSpec(ctx, q, rc)
	if err != nil {
		return Outcome{}, err
	}
	ws, err := store.GetWorkspaceByTask(ctx, q, rc.task.TaskID)
	if errors.Is(err, store.ErrNotFound) || err == nil && ws.HostID != rc.run.HostID {
		// The task started over and gave up the workspace this run was
		// laid out in (D62).
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the task gave up the workspace of run %s", rc.run.RunID)
	} else if err != nil {
		return Outcome{}, err
	}
	if ws.OccupiedBy != "" && ws.OccupiedBy != rc.task.TaskID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "workspace is occupied by another task")
	}
	if ws.ProviderID != "" && p.ProviderID != ws.ProviderID || ws.ProviderInstanceID != "" && p.ProviderInstanceID != ws.ProviderInstanceID || ws.Path != "" && p.Path != ws.Path {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "workspace provider identity or path changed")
	}
	if p.Path != "" {
		if err := tx.RecordWorkspaceLocation(ctx, ws.WorkspaceID, p.ProviderID, p.ProviderInstanceID, p.Path, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	if len(p.Baseline) > 0 {
		if !json.Valid(p.Baseline) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid workspace baseline")
		}
		if err := tx.RecordWorkspaceBaseline(ctx, ws.WorkspaceID, rc.task.TaskID, string(p.Baseline), x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	recorded, err := store.ListMountFacts(ctx, q, ws.WorkspaceID)
	if err != nil {
		return Outcome{}, err
	}
	// The workspace provider names the branch: the runner's own layout
	// uses the task branch, orca its own (D65). The first report records
	// it, and later ones must agree.
	reported := map[string]protocol.ResolvedMount{}
	for _, m := range p.Mounts {
		if !shaPattern.MatchString(m.ResolvedBaseSHA) || m.ResolvedFrom == "" || !domain.ValidBranch(m.Branch) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "mount %q needs a full base sha, its source and a valid branch", m.Path)
		}
		reported[m.Path] = m
	}
	now := x.Now.UnixMilli()
	added := 0
	for _, m := range spec.Workspace.Mounts {
		r, ok := reported[m.Path]
		if !ok {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "mount %q is missing from the report", m.Path)
		}
		delete(reported, m.Path)
		if prev, ok := recorded[m.Path]; ok {
			if ws.Mode != domain.WorkspaceReuse && (prev.ResolvedBaseSHA != r.ResolvedBaseSHA || prev.Branch != r.Branch) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "mount %q was already recorded with base %s on %s", m.Path, prev.ResolvedBaseSHA, prev.Branch)
			}
			continue
		}
		base, err := json.Marshal(m.Base)
		if err != nil {
			return Outcome{}, fmt.Errorf("app: encode base: %w", err)
		}
		if err := tx.InsertMountFact(ctx, ws.WorkspaceID, store.MountFact{
			Path: m.Path, Repo: m.Repo, BaseJSON: string(base), ResolvedBaseSHA: r.ResolvedBaseSHA,
			ResolvedFrom: r.ResolvedFrom, Branch: r.Branch,
		}, now); err != nil {
			return Outcome{}, err
		}
		added++
	}
	if len(reported) > 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "the report has mounts the spec does not")
	}
	if ws.Status != store.WorkspaceReady {
		if err := tx.SetWorkspaceStatus(ctx, ws.WorkspaceID, store.WorkspaceReady, now); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{
		Result: map[string]any{"workspace_id": ws.WorkspaceID, "mounts_added": added},
		Events: []Event{{ObjectType: "workspace", ObjectID: ws.WorkspaceID, Kind: "workspace.ready", Body: map[string]any{"run_id": rc.run.RunID, "mounts_added": added}}},
	}, nil
}

// runSpec returns the spec version a run was created for.
func runSpec(ctx context.Context, q store.Querier, rc runContext) (domain.Spec, error) {
	body, err := store.GetSpec(ctx, q, rc.task.TaskID, rc.action.TargetSpecVersion)
	if err != nil {
		return domain.Spec{}, err
	}
	var spec domain.Spec
	if err := json.Unmarshal([]byte(body), &spec); err != nil {
		return domain.Spec{}, fmt.Errorf("app: decode spec of %s: %w", rc.task.TaskID, err)
	}
	return spec, nil
}

// Release handles runs.release: the runner gives up a run that will not
// produce a result, for example after a stop verdict, a crashed worker or
// no changes. The run ends as failed and frees its slot and workspace; if
// it was the task's current work, the task waits for the owner (D31).
type Release struct{}

// Operation implements Command.
func (Release) Operation() string { return protocol.OpRunsRelease }

// Authorize implements Command.
func (Release) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRun || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "only the run itself may release it")
	}
	return nil
}

// Apply implements Command.
func (Release) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ReleasePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Reason == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "reason is required")
	}
	rc, err := loadRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if err := endRun(ctx, x, tx, rc.run, protocol.RunFailed); err != nil {
		return Outcome{}, err
	}
	if rc.action.Status == protocol.ActionDispatched {
		if err := tx.SetActionStatus(ctx, rc.action.ActionID, protocol.ActionDone, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	d := domain.RunFailed(toDomain(rc.task, rc.spec.Budget()), rc.fact(), p.Reason)
	_, events, err := applyDecision(ctx, x, tx, rc.task, d, map[string]any{"on": "run.failed", "run_id": rc.run.RunID})
	if err != nil {
		return Outcome{}, err
	}
	events = append(events, Event{ObjectType: "run", ObjectID: rc.run.RunID, Kind: "run.released", Body: map[string]any{"reason": p.Reason}})
	return Outcome{Result: map[string]any{"run_id": rc.run.RunID, "observed": protocol.RunFailed, "task": newTaskResult(d.Task, nil)}, Events: events}, nil
}

// endRun moves a run to a terminal state and releases its lease. A result
// or a release from the run itself proves it ran, so it may end a run that
// is still starting, was interrupted and resumed, or is unknown.
func endRun(ctx context.Context, x *Exec, tx *store.Tx, run store.RunRow, to protocol.RunObserved) error {
	from := run.ObservedState
	if from == protocol.RunFinished || from == protocol.RunFailed {
		return protocol.Errorf(protocol.CodeInvalidTransition, "run %s already ended as %s", run.RunID, from)
	}
	if from == protocol.RunStarting || from == protocol.RunInterrupted {
		from = protocol.RunRunning
	}
	if !domain.RunTransitionAllowed(from, to, true) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "run %s cannot end as %s from %s", run.RunID, to, run.ObservedState)
	}
	now := x.Now.UnixMilli()
	if err := tx.UpdateRunObserved(ctx, run.RunID, to, now); err != nil {
		return err
	}
	lease, err := store.GetLeaseByRun(ctx, tx.Q(), run.RunID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return err
	case lease.Status != store.LeaseReleased:
		if err := tx.SetLease(ctx, lease.LeaseID, lease.Status, store.LeaseReleased, lease.ExpiresAt, now); err != nil {
			return err
		}
	}
	return nil
}

// Heartbeat renews the leases of the runs a runner holds and tells it what
// tend wants each run to do. It is a system maintenance write: renewal is
// conditional on the lease still being active, so it is idempotent without
// a request record. A lease that lapsed is not renewed; the runner must
// fence itself and reconcile through hello.
func (a *App) Heartbeat(ctx context.Context, p protocol.Principal, refs []protocol.LeaseRef) (protocol.HeartbeatResult, error) {
	res := protocol.HeartbeatResult{LeaseTTLMillis: a.leaseTTL.Milliseconds(), Runs: []protocol.LeaseState{}}
	if p.Kind != protocol.PrincipalRunner {
		return res, protocol.Errorf(protocol.CodeForbidden, "only runners send heartbeats")
	}
	now := a.now()
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		q := tx.Q()
		host, err := store.GetHost(ctx, q, p.Subject)
		if err != nil || host.Status != store.HostActive {
			return protocol.Errorf(protocol.CodeForbidden, "host %s is not active", p.Subject)
		}
		if err := tx.TouchHost(ctx, host.HostID, now.UnixMilli()); err != nil {
			return err
		}
		for _, ref := range refs {
			state := protocol.LeaseState{RunID: ref.RunID, Desired: protocol.DesiredStopped}
			rc, err := loadRun(ctx, q, ref.RunID)
			if protocol.CodeOf(err) == protocol.CodeNotFound {
				res.Runs = append(res.Runs, state)
				continue
			}
			if err != nil {
				return err
			}
			lease, err := store.GetLeaseByRun(ctx, q, ref.RunID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err == nil && rc.run.HostID == host.HostID && lease.LeaseID == ref.LeaseID && lease.Status == store.LeaseActive {
				if err := tx.SetLease(ctx, lease.LeaseID, store.LeaseActive, store.LeaseActive, now.Add(a.leaseTTL).UnixMilli(), now.UnixMilli()); err != nil {
					return err
				}
				state.Renewed = true
				state.Desired = desiredFor(rc.run, rc.action, rc.task)
			}
			res.Runs = append(res.Runs, state)
		}
		return nil
	})
	return res, err
}

var eventKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// RecordRunEvents stores facts a run reported. Each event is deduplicated by
// (run, seq) through the inbox, so a resent batch has no further effect;
// this makes it a system maintenance write without a request record. Every
// event is kept as a run log, and the state kinds also move the run's
// observed state when the transition is allowed.
func (a *App) RecordRunEvents(ctx context.Context, p protocol.Principal, runID string, events []protocol.RunEvent) (protocol.EventsResult, error) {
	var res protocol.EventsResult
	if p.Kind != protocol.PrincipalRun || p.Subject != runID {
		return res, protocol.Errorf(protocol.CodeForbidden, "a run may only report its own events")
	}
	for _, e := range events {
		if e.Seq < 1 || !eventKindPattern.MatchString(e.Kind) || (len(e.Body) > 0 && !json.Valid(e.Body)) {
			return res, protocol.Errorf(protocol.CodeInvalidRequest, "event %d is malformed", e.Seq)
		}
	}
	now := a.now().UnixMilli()
	changed := false
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		run, err := store.GetRun(ctx, tx.Q(), runID)
		if err != nil {
			return errNotFound(err, "run %s does not exist", runID)
		}
		res.Accepted = 0
		for _, e := range events {
			fresh, err := tx.InsertInbox(ctx, "run:"+runID, strconv.FormatInt(e.Seq, 10), now)
			if err != nil {
				return err
			}
			if !fresh {
				continue
			}
			res.Accepted++
			if err := tx.InsertRunLog(ctx, runID, e, now); err != nil {
				return err
			}
			// Events only move a live run between running and waiting, or
			// resume an interrupted one. Leaving starting goes through
			// runs.report_started, which also applies the task rules.
			to := map[string]protocol.RunObserved{protocol.EventRunRunning: protocol.RunRunning, protocol.EventRunWaiting: protocol.RunWaiting}[e.Kind]
			live := run.ObservedState == protocol.RunRunning || run.ObservedState == protocol.RunWaiting || run.ObservedState == protocol.RunInterrupted
			if to == "" || !live || !domain.RunTransitionAllowed(run.ObservedState, to, false) {
				continue
			}
			if err := tx.UpdateRunObserved(ctx, runID, to, now); err != nil {
				return err
			}
			body, err := json.Marshal(map[string]any{"from": run.ObservedState, "to": to, "seq": e.Seq})
			if err != nil {
				return fmt.Errorf("app: encode run event: %w", err)
			}
			if _, err := tx.InsertEvent(ctx, store.EventRow{
				EventID: a.ids.New(protocol.PrefixEvent), Source: "runner", ObjectType: "run", ObjectID: runID,
				Kind: "run.observed", BodyJSON: string(body), CreatedAt: now,
			}); err != nil {
				return err
			}
			run.ObservedState = to
			changed = true
		}
		return nil
	})
	if err == nil && res.Accepted > 0 {
		a.logs.Notify()
	}
	if err == nil && changed {
		a.notifyChange()
	}
	return res, err
}

// ExpireLeases marks leases that ran out without renewal as lapsed and their
// runs as unknown. Losing contact does not prove a run stopped, so its
// workspace stays occupied until the runner reconciles or the owner
// decides. It is a system maintenance write, conditional on each lease
// still being active.
func (a *App) ExpireLeases(ctx context.Context) (int, error) {
	now := a.now().UnixMilli()
	n := 0
	lapsed := 0
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		expired, err := store.ListExpiredLeases(ctx, tx.Q(), now)
		if err != nil {
			return err
		}
		lapsed = len(expired)
		for _, l := range expired {
			if err := tx.SetLease(ctx, l.LeaseID, store.LeaseActive, store.LeaseLapsed, l.ExpiresAt, now); err != nil {
				return err
			}
			run, err := store.GetRun(ctx, tx.Q(), l.RunID)
			if err != nil {
				return err
			}
			if !domain.RunTransitionAllowed(run.ObservedState, protocol.RunUnknown, false) {
				continue
			}
			if err := tx.UpdateRunObserved(ctx, run.RunID, protocol.RunUnknown, now); err != nil {
				return err
			}
			body, err := json.Marshal(map[string]any{"from": run.ObservedState, "reason": "lease_expired", "host_id": l.HostID})
			if err != nil {
				return fmt.Errorf("app: encode lease event: %w", err)
			}
			if _, err := tx.InsertEvent(ctx, store.EventRow{
				EventID: a.ids.New(protocol.PrefixEvent), Source: "app", ObjectType: "run", ObjectID: run.RunID,
				Kind: "run.unknown", BodyJSON: string(body), CreatedAt: now,
			}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err == nil && lapsed > 0 {
		a.notifyChange()
	}
	return n, err
}

// RunCredential returns the lease a run token must be derived from. Only an
// active or lapsed lease authenticates the run: a lapsed run may still
// report its result.
func (a *App) RunCredential(ctx context.Context, runID string) (leaseID, hostID string, role protocol.RunRole, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		run, err := store.GetRun(ctx, tx.Q(), runID)
		if err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), runID)
		if err != nil {
			return err
		}
		if lease.Status == store.LeaseReleased {
			return store.ErrNotFound
		}
		leaseID, hostID, role = lease.LeaseID, run.HostID, run.Role
		return nil
	})
	return leaseID, hostID, role, err
}

// Host returns a configured host for authentication.
func (a *App) Host(ctx context.Context, hostID string) (store.HostRow, error) {
	var h store.HostRow
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		h, err = store.GetHost(ctx, tx.Q(), hostID)
		return err
	})
	return h, err
}
