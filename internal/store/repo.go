package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ErrNotFound is returned when a looked-up row does not exist.
var ErrNotFound = errors.New("store: not found")

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("store: %s: %w", what, err)
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// RequestRecord is the dedup record of a side-effecting request.
type RequestRecord struct {
	Domain          string
	RequestID       string
	Operation       string
	PayloadHash     string
	PrincipalJSON   string
	ParentRequestID string
	TraceID         string
	Status          protocol.OpStatus
	ResultJSON      string
	ErrorCode       string
	ErrorMessage    string
	ErrorDetails    string
	// Reason is the caller's note on why it made the request.
	Reason    string
	CreatedAt int64
}

// GetRequest returns the record for (domain, requestID), or ErrNotFound.
func GetRequest(ctx context.Context, q Querier, domain, requestID string) (RequestRecord, error) {
	r := RequestRecord{Domain: domain, RequestID: requestID}
	var parent, result, code, msg, details, reason sql.NullString
	err := q.QueryRowContext(ctx, `SELECT operation, payload_hash, principal_json, parent_request_id, trace_id, status,
		result_json, error_code, error_message, error_details, reason, created_at
		FROM request_records WHERE domain = ? AND request_id = ?`, domain, requestID).
		Scan(&r.Operation, &r.PayloadHash, &r.PrincipalJSON, &parent, &r.TraceID, &r.Status, &result, &code, &msg, &details, &reason, &r.CreatedAt)
	if err != nil {
		return RequestRecord{}, notFound(err, "request record")
	}
	r.ParentRequestID, r.ResultJSON, r.ErrorCode, r.ErrorMessage, r.ErrorDetails = parent.String, result.String, code.String, msg.String, details.String
	r.Reason = reason.String
	return r, nil
}

// InsertRequest stores a new request record.
func (t *Tx) InsertRequest(ctx context.Context, r RequestRecord) error {
	_, err := t.Exec(ctx, `INSERT INTO request_records (domain, request_id, operation, payload_hash, principal_json,
		parent_request_id, trace_id, status, result_json, error_code, error_message, error_details, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Domain, r.RequestID, r.Operation, r.PayloadHash, r.PrincipalJSON, nullString(r.ParentRequestID), r.TraceID,
		r.Status, nullString(r.ResultJSON), nullString(r.ErrorCode), nullString(r.ErrorMessage), nullString(r.ErrorDetails),
		nullString(r.Reason), r.CreatedAt, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: insert request record: %w", err)
	}
	return nil
}

// TaskRow is the stored task projection.
type TaskRow struct {
	TaskID              string
	Title               string
	Project             string
	Lifecycle           protocol.TaskLifecycle
	ControlMode         protocol.ControlMode
	ControlRevision     int64
	SpecVersion         int64
	Rating              protocol.Rating
	BlockReason         protocol.BlockReason
	ImplementGeneration int64
	CurrentArtifactID   string
	// WorkspaceName is reserved at creation and never changes.
	WorkspaceName string
	// WorkspaceAssigned reports that the workspace exists on a host. It is
	// derived from the workspaces table and not written by UpdateTask.
	WorkspaceAssigned bool
	CreatedAt         int64
	UpdatedAt         int64
}

// GetTask returns the task, or ErrNotFound.
func GetTask(ctx context.Context, q Querier, taskID string) (TaskRow, error) {
	r := TaskRow{TaskID: taskID}
	var artifact, workspace sql.NullString
	err := q.QueryRowContext(ctx, `SELECT title, project, lifecycle, control_mode, control_revision, spec_version, rating,
		block_reason, implement_generation, current_artifact_id, workspace_name,
		EXISTS (SELECT 1 FROM task_workspace_bindings w WHERE w.task_id = tasks.task_id), created_at, updated_at
		FROM tasks WHERE task_id = ?`, taskID).
		Scan(&r.Title, &r.Project, &r.Lifecycle, &r.ControlMode, &r.ControlRevision, &r.SpecVersion, &r.Rating, &r.BlockReason,
			&r.ImplementGeneration, &artifact, &workspace, &r.WorkspaceAssigned, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return TaskRow{}, notFound(err, "task "+taskID)
	}
	r.CurrentArtifactID, r.WorkspaceName = artifact.String, workspace.String
	return r, nil
}

// InsertTask stores a new task.
func (t *Tx) InsertTask(ctx context.Context, r TaskRow) error {
	_, err := t.Exec(ctx, `INSERT INTO tasks (task_id, title, project, lifecycle, control_mode, control_revision, spec_version,
		priority, rating, block_reason, implement_generation, current_artifact_id, workspace_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.Title, r.Project, r.Lifecycle, r.ControlMode, r.ControlRevision, r.SpecVersion, r.Rating, r.BlockReason,
		r.ImplementGeneration, nullString(r.CurrentArtifactID), nullString(r.WorkspaceName), r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: insert task: %w", err)
	}
	return nil
}

// UpdateTask overwrites the mutable fields of a task.
func (t *Tx) UpdateTask(ctx context.Context, r TaskRow) error {
	res, err := t.Exec(ctx, `UPDATE tasks SET project = ?, lifecycle = ?, control_mode = ?, control_revision = ?, spec_version = ?,
		rating = ?, block_reason = ?, implement_generation = ?, current_artifact_id = ?, updated_at = ? WHERE task_id = ?`,
		r.Project, r.Lifecycle, r.ControlMode, r.ControlRevision, r.SpecVersion, r.Rating, r.BlockReason, r.ImplementGeneration,
		nullString(r.CurrentArtifactID), r.UpdatedAt, r.TaskID)
	if err != nil {
		return fmt.Errorf("store: update task: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("store: update task %s: %w", r.TaskID, ErrNotFound)
	}
	return nil
}

// InsertSpec stores an immutable spec version.
func (t *Tx) InsertSpec(ctx context.Context, taskID string, version int64, bodyJSON, sourceMessageID string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO task_specs (task_id, spec_version, body_json, source_message_id, created_at)
		VALUES (?, ?, ?, ?, ?)`, taskID, version, bodyJSON, nullString(sourceMessageID), now)
	if err != nil {
		return fmt.Errorf("store: insert spec: %w", err)
	}
	return nil
}

// GetSpec returns the JSON body of a spec version, or ErrNotFound.
func GetSpec(ctx context.Context, q Querier, taskID string, version int64) (string, error) {
	var body string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM task_specs WHERE task_id = ? AND spec_version = ?`, taskID, version).Scan(&body)
	if err != nil {
		return "", notFound(err, "spec")
	}
	return body, nil
}

// InsertPlanCommit claims a planning commit of a source message. A replay
// returns a unique violation.
func (t *Tx) InsertPlanCommit(ctx context.Context, sourceMessageID string, seq int64, requestID string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO plan_commits (source_message_id, commit_seq, request_id, created_at) VALUES (?, ?, ?, ?)`,
		sourceMessageID, seq, requestID, now)
	if err != nil {
		return fmt.Errorf("store: insert plan commit: %w", err)
	}
	return nil
}

// GetPlanCommitRequest returns the request that claimed a planning commit.
func GetPlanCommitRequest(ctx context.Context, q Querier, sourceMessageID string, seq int64) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT request_id FROM plan_commits WHERE source_message_id = ? AND commit_seq = ?`,
		sourceMessageID, seq).Scan(&id)
	if err != nil {
		return "", notFound(err, "plan commit")
	}
	return id, nil
}

// ActionRow is a stored plan action.
type ActionRow struct {
	ActionID          string
	TaskID            string
	Kind              protocol.ActionKind
	EffectKey         string
	TriggerJSON       string
	TargetSpecVersion int64
	TargetArtifactID  string
	AuthorizationJSON string
	Generation        int64
	Status            protocol.ActionStatus
	PlanCommitRef     string
	CreatedRequestID  string
	CreatedAt         int64
}

// InsertAction stores a plan action. A second action with the same effect
// key returns a unique violation.
func (t *Tx) InsertAction(ctx context.Context, a ActionRow) error {
	_, err := t.Exec(ctx, `INSERT INTO plan_actions (action_id, task_id, kind, effect_key, trigger_json, target_spec_version,
		target_artifact_id, authorization_json, generation, status, plan_commit_ref, created_request_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ActionID, a.TaskID, a.Kind, a.EffectKey, a.TriggerJSON, a.TargetSpecVersion, nullString(a.TargetArtifactID),
		a.AuthorizationJSON, a.Generation, a.Status, nullString(a.PlanCommitRef), a.CreatedRequestID, a.CreatedAt, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: insert action: %w", err)
	}
	return nil
}

const actionColumns = `action_id, task_id, kind, effect_key, trigger_json, target_spec_version, target_artifact_id,
	authorization_json, generation, status, plan_commit_ref, created_request_id, created_at`

func scanAction(row interface{ Scan(...any) error }) (ActionRow, error) {
	var a ActionRow
	var artifact, commit sql.NullString
	err := row.Scan(&a.ActionID, &a.TaskID, &a.Kind, &a.EffectKey, &a.TriggerJSON, &a.TargetSpecVersion, &artifact,
		&a.AuthorizationJSON, &a.Generation, &a.Status, &commit, &a.CreatedRequestID, &a.CreatedAt)
	a.TargetArtifactID, a.PlanCommitRef = artifact.String, commit.String
	return a, err
}

// GetAction returns an action, or ErrNotFound.
func GetAction(ctx context.Context, q Querier, actionID string) (ActionRow, error) {
	a, err := scanAction(q.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM plan_actions WHERE action_id = ?`, actionID))
	if err != nil {
		return ActionRow{}, notFound(err, "action "+actionID)
	}
	return a, nil
}

// GetActionByEffectKey returns the action holding an effect key, or
// ErrNotFound.
func GetActionByEffectKey(ctx context.Context, q Querier, effectKey string) (ActionRow, error) {
	a, err := scanAction(q.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM plan_actions WHERE effect_key = ?`, effectKey))
	if err != nil {
		return ActionRow{}, notFound(err, "action")
	}
	return a, nil
}

// ListActions returns the actions of a task in creation order.
func ListActions(ctx context.Context, q Querier, taskID string) ([]ActionRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+actionColumns+` FROM plan_actions WHERE task_id = ? ORDER BY created_at, action_id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: list actions: %w", err)
	}
	defer rows.Close()
	var out []ActionRow
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan action: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LatestAcceptGeneration returns the newest acceptance generation of an
// artifact under a spec version, or zero.
func LatestAcceptGeneration(ctx context.Context, q Querier, taskID string, specVersion int64, artifactID string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation), 0) FROM plan_actions
		WHERE task_id = ? AND kind = 'accept' AND target_spec_version = ? AND target_artifact_id = ?`, taskID, specVersion, artifactID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: latest accept generation: %w", err)
	}
	return n, nil
}

// SupersedeActionsBefore marks the task's not-yet-dispatched actions for
// spec versions older than version as superseded.
func (t *Tx) SupersedeActionsBefore(ctx context.Context, taskID string, version int64, now int64) error {
	_, err := t.Exec(ctx, `UPDATE plan_actions SET status = ?, updated_at = ?
		WHERE task_id = ? AND target_spec_version < ? AND status IN (?, ?)`,
		protocol.ActionSuperseded, now, taskID, version, protocol.ActionPending, protocol.ActionReady)
	if err != nil {
		return fmt.Errorf("store: supersede actions: %w", err)
	}
	return nil
}

// CancelOpenActions cancels every action of the task that is not finished.
func (t *Tx) CancelOpenActions(ctx context.Context, taskID string, now int64) error {
	_, err := t.Exec(ctx, `UPDATE plan_actions SET status = ?, updated_at = ?
		WHERE task_id = ? AND status IN (?, ?, ?)`,
		protocol.ActionCancelled, now, taskID, protocol.ActionPending, protocol.ActionReady, protocol.ActionDispatched)
	if err != nil {
		return fmt.Errorf("store: cancel actions: %w", err)
	}
	return nil
}

// RunRow is a stored run.
type RunRow struct {
	Protocol      int64
	RunID         string
	TaskID        string
	ActionID      string
	Generation    int64
	Role          protocol.RunRole
	HostID        string
	DesiredState  protocol.RunDesired
	ObservedState protocol.RunObserved
	CreatedAt     int64
}

// InsertRun stores a run. A second run for the same action generation
// returns a unique violation.
func (t *Tx) InsertRun(ctx context.Context, r RunRow) error {
	_, err := t.Exec(ctx, `INSERT INTO runs (run_id, task_id, action_id, generation, role, host_id, desired_state,
		observed_state, created_at, updated_at, protocol) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.TaskID, r.ActionID, r.Generation, r.Role, nullString(r.HostID), r.DesiredState, r.ObservedState,
		r.CreatedAt, r.CreatedAt, r.Protocol)
	if err != nil {
		return fmt.Errorf("store: insert run: %w", err)
	}
	return nil
}

const runColumns = `run_id, task_id, action_id, generation, role, host_id, desired_state, observed_state, created_at, protocol`

func scanRun(row interface{ Scan(...any) error }) (RunRow, error) {
	var r RunRow
	var host sql.NullString
	err := row.Scan(&r.RunID, &r.TaskID, &r.ActionID, &r.Generation, &r.Role, &host, &r.DesiredState, &r.ObservedState, &r.CreatedAt, &r.Protocol)
	r.HostID = host.String
	return r, err
}

// GetRun returns a run, or ErrNotFound.
func GetRun(ctx context.Context, q Querier, runID string) (RunRow, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE run_id = ?`, runID))
	if err != nil {
		return RunRow{}, notFound(err, "run "+runID)
	}
	return r, nil
}

// UpdateRunObserved records a newly confirmed observed state.
func (t *Tx) UpdateRunObserved(ctx context.Context, runID string, state protocol.RunObserved, now int64) error {
	_, err := t.Exec(ctx, `UPDATE runs SET observed_state = ?, last_confirmed_at = ?, updated_at = ? WHERE run_id = ?`,
		state, now, now, runID)
	if err != nil {
		return fmt.Errorf("store: update run: %w", err)
	}
	return nil
}

// InsertArtifact stores an immutable artifact reference.
func (t *Tx) InsertArtifact(ctx context.Context, artifactID, taskID, runID, kind, refJSON, digest string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO artifacts (artifact_id, task_id, run_id, kind, ref_json, digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, artifactID, taskID, runID, kind, refJSON, digest, now)
	if err != nil {
		return fmt.Errorf("store: insert artifact: %w", err)
	}
	return nil
}

// GetArtifactRef returns an artifact's reference document, or ErrNotFound.
func GetArtifactRef(ctx context.Context, q Querier, artifactID string) (string, error) {
	var ref string
	if err := q.QueryRowContext(ctx, `SELECT ref_json FROM artifacts WHERE artifact_id = ?`, artifactID).Scan(&ref); err != nil {
		return "", notFound(err, "artifact "+artifactID)
	}
	return ref, nil
}

// InsertAcceptance stores an acceptance verdict.
func (t *Tx) InsertAcceptance(ctx context.Context, id, taskID, runID string, specVersion int64, artifactID string,
	verdict protocol.Verdict, evidenceJSON, issuesJSON string, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO acceptances (acceptance_id, task_id, run_id, spec_version, artifact_id, verdict,
		evidence_json, issues_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, taskID, runID, specVersion, artifactID, verdict, evidenceJSON, issuesJSON, now)
	if err != nil {
		return fmt.Errorf("store: insert acceptance: %w", err)
	}
	return nil
}

// InsertDecision stores an open decision. effectsJSON maps options to what
// tend does when one is chosen, and askedRevision is the task's control
// revision when it asked; a planner's decision has neither.
func (t *Tx) InsertDecision(ctx context.Context, id, taskID, question, optionsJSON, effectsJSON string, askedRevision *int64, now int64) error {
	_, err := t.Exec(ctx, `INSERT INTO decisions (decision_id, task_id, question, options_json, effects_json, asked_revision, scope,
		version, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'task', 1, 'open', ?, ?)`,
		id, nullString(taskID), question, optionsJSON, effectsJSON, askedRevision, now, now)
	if err != nil {
		return fmt.Errorf("store: insert decision: %w", err)
	}
	return nil
}

// EventRow is a ledger event to append.
type EventRow struct {
	EventID       string
	Source        string
	ObjectType    string
	ObjectID      string
	ObjectVersion int64
	Kind          string
	BodyJSON      string
	RequestID     string
	TraceID       string
	// ActorJSON is the principal of the request that recorded the event,
	// if any.
	ActorJSON string
	CreatedAt int64
}

// InsertEvent appends an event and returns its sequence number.
func (t *Tx) InsertEvent(ctx context.Context, e EventRow) (int64, error) {
	res, err := t.Exec(ctx, `INSERT INTO events (event_id, source, object_type, object_id, object_version, kind, body_json,
		request_id, trace_id, actor_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.EventID, e.Source, e.ObjectType, e.ObjectID, e.ObjectVersion, e.Kind, e.BodyJSON, nullString(e.RequestID),
		nullString(e.TraceID), nullString(e.ActorJSON), e.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("store: insert event: %w", err)
	}
	return res.LastInsertId()
}

// OutboxRow is a message to deliver after the transaction commits.
type OutboxRow struct {
	OutboxID        string
	RequestID       string
	ParentRequestID string
	Destination     string
	Operation       string
	PayloadJSON     string
	CreatedAt       int64
}

// InsertOutbox queues a message for delivery. Its position in the queue is
// assigned here, inside the transaction, so delivery order follows commit
// order.
func (t *Tx) InsertOutbox(ctx context.Context, o OutboxRow) error {
	_, err := t.Exec(ctx, `INSERT INTO outbox (outbox_id, request_id, parent_request_id, destination, operation, payload_json,
		status, attempts, next_attempt_at, created_at, updated_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM outbox))`,
		o.OutboxID, o.RequestID, nullString(o.ParentRequestID), o.Destination, o.Operation, o.PayloadJSON, o.CreatedAt,
		o.CreatedAt, o.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: insert outbox: %w", err)
	}
	return nil
}

// Savepoint starts a named savepoint inside the transaction.
func (t *Tx) Savepoint(ctx context.Context, name string) error {
	_, err := t.Exec(ctx, "SAVEPOINT "+name)
	return err
}

// RollbackTo undoes everything since the named savepoint and releases it.
func (t *Tx) RollbackTo(ctx context.Context, name string) error {
	if _, err := t.Exec(ctx, "ROLLBACK TO "+name); err != nil {
		return err
	}
	_, err := t.Exec(ctx, "RELEASE "+name)
	return err
}

// Release keeps the changes made since the named savepoint.
func (t *Tx) Release(ctx context.Context, name string) error {
	_, err := t.Exec(ctx, "RELEASE "+name)
	return err
}

// DecisionRow is a question for the owner.
type DecisionRow struct {
	Worker            *protocol.WorkerQuestionSource
	DecisionID        string
	TaskID            string
	Question          string
	OptionsJSON       string
	EffectsJSON       string
	AskedRevision     sql.NullInt64
	Version           int64
	Status            string
	AskedMessageID    string
	AnsweredMessageID string
	AnswerJSON        string
}

// Decision states.
const (
	DecisionOpen      = "open"
	DecisionAnswered  = "answered"
	DecisionCancelled = "cancelled"
)

// GetDecision returns a decision, or ErrNotFound.
func GetDecision(ctx context.Context, q Querier, decisionID string) (DecisionRow, error) {
	d := DecisionRow{DecisionID: decisionID}
	var task, asked, answered, answer sql.NullString
	err := q.QueryRowContext(ctx, `SELECT task_id, question, options_json, effects_json, asked_revision, version, status,
		asked_message_id, answered_message_id, answer_json FROM decisions WHERE decision_id = ?`, decisionID).
		Scan(&task, &d.Question, &d.OptionsJSON, &d.EffectsJSON, &d.AskedRevision, &d.Version, &d.Status, &asked, &answered, &answer)
	if err != nil {
		return DecisionRow{}, notFound(err, "decision "+decisionID)
	}
	d.TaskID, d.AskedMessageID, d.AnsweredMessageID, d.AnswerJSON = task.String, asked.String, answered.String, answer.String
	w, workerErr := GetWorkerQuestionRow(ctx, q, decisionID)
	if workerErr == nil {
		run, err := GetRun(ctx, q, w.RunID)
		if err != nil {
			return DecisionRow{}, err
		}
		d.Worker = &protocol.WorkerQuestionSource{RunID: w.RunID, Role: run.Role, SpecVersion: w.SpecVersion, QuestionKey: w.QuestionKey, Evidence: w.Evidence, QuestionMessageID: d.AskedMessageID}
	} else if !errors.Is(workerErr, ErrNotFound) {
		return DecisionRow{}, workerErr
	}
	return d, nil
}

// SetDecisionAskedMessage records the conversation message that asked the
// question.
func (t *Tx) SetDecisionAskedMessage(ctx context.Context, decisionID, messageID string) error {
	_, err := t.Exec(ctx, `UPDATE decisions SET asked_message_id = ? WHERE decision_id = ?`, messageID, decisionID)
	if err != nil {
		return fmt.Errorf("store: set asked message: %w", err)
	}
	return nil
}

// AnswerDecision closes an open decision at the given version. It returns
// ErrStale when the decision moved on.
func (t *Tx) AnswerDecision(ctx context.Context, decisionID string, version int64, answerJSON, messageID string, now int64) error {
	res, err := t.Exec(ctx, `UPDATE decisions SET status = 'answered', version = version + 1, answer_json = ?,
		answered_message_id = ?, updated_at = ? WHERE decision_id = ? AND status = 'open' AND version = ?`,
		answerJSON, nullString(messageID), now, decisionID, version)
	if err != nil {
		return fmt.Errorf("store: answer decision: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrStale
	}
	return nil
}
