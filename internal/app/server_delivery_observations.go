package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const serverDeliveryLease = 90 * time.Second

// Terminal merges are eligible only while an existing bounded failed read has
// a retry scheduled. Successful or exhausted terminal facts never poll forever.
const serverDeliveryAutomaticMember = `(r.active=1 AND (COALESCE(json_extract(o.observation_json,'$.state'),'') NOT IN ('merged','closed') OR (json_extract(o.observation_json,'$.state')='merged' AND COALESCE(json_extract(o.observation_json,'$.error_kind'),'')<>'' AND COALESCE(json_extract(o.observation_json,'$.retry_at'),0)>0)))`

// WithServerDeliveryObservations transfers new read ownership explicitly. The
// legacy runner request formats remain valid; old in-flight reads are fenced by
// their original deadline and retained identity, never silently replayed.
func WithServerDeliveryObservations(enabled bool) Option {
	return func(a *App) { a.serverDeliveryObservations = enabled }
}

type ServerDeliveryQuery struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
}

type claimServerDelivery struct{}

func (claimServerDelivery) Operation() string { return "server.deliveries_claim" }
func (claimServerDelivery) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}

func (a *App) ClaimServerDelivery(ctx context.Context) (*ServerDeliveryQuery, error) {
	if !a.serverDeliveryObservations {
		return nil, nil
	}
	var due bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		if paused, err := upgradePausesMain(ctx, tx.Q()); err != nil || paused {
			return err
		}
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_delivery_jobs WHERE next_at<=? AND (nonce='' OR query_until<=?))`, a.now().UnixMilli(), a.now().UnixMilli()).Scan(&due)
	})
	if err != nil || !due {
		return nil, err
	}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: "server.deliveries_claim", Target: protocol.Target{Type: "delivery_observer"}, Payload: json.RawMessage(`{}`), Reason: "Claim one due shared provider read."}, claimServerDelivery{})
	if err != nil {
		return nil, err
	}
	if op.Error != nil {
		return nil, op.Error
	}
	var result *ServerDeliveryQuery
	if len(op.Result) > 0 {
		err = json.Unmarshal(op.Result, &result)
	}
	return result, err
}

func (claimServerDelivery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if !x.serverDeliveryObservations {
		return Outcome{NoEffect: true}, nil
	}
	if paused, err := upgradePausesMain(ctx, tx.Q()); err != nil {
		return Outcome{}, err
	} else if paused {
		return Outcome{NoEffect: true}, nil
	}
	var q ServerDeliveryQuery
	var nonce, cache string
	var attempts int
	var refreshRevision, observedRefreshRevision int64
	now := x.Now.UnixMilli()
	err := tx.Q().QueryRowContext(ctx, `SELECT provider,repository,number,nonce,attempts,observation_json,refresh_revision,observed_refresh_revision FROM server_delivery_jobs WHERE next_at<=? AND (nonce='' OR query_until<=?) ORDER BY next_at,provider,repository,number LIMIT 1`, now, now).Scan(&q.Provider, &q.Repository, &q.Number, &nonce, &attempts, &cache, &refreshRevision, &observedRefreshRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return Outcome{NoEffect: true}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if nonce != "" {
		return finishServerDelivery(ctx, x, tx, nonce, nil, "timeout", nil)
	}
	// A previous runner may still be physically reading this same PR. Stop new
	// runner claims, wait its bounded lease, then retain and fence its identity.
	var oldUntil int64
	err = tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(query_until),0) FROM (SELECT o.query_until FROM server_delivery_references r JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id WHERE r.provider=? AND r.repository=? AND r.number=? AND o.nonce<>'' AND o.host_id<>'server' UNION ALL SELECT old.query_until FROM retired_runner_delivery_queries old JOIN server_delivery_references r ON r.issue_id=old.issue_id AND r.delivery_id=old.delivery_id WHERE r.provider=? AND r.repository=? AND r.number=?)`, q.Provider, q.Repository, q.Number, q.Provider, q.Repository, q.Number).Scan(&oldUntil)
	if err != nil {
		return Outcome{}, err
	}
	if oldUntil > now {
		_, err = tx.Exec(ctx, `UPDATE server_delivery_jobs SET next_at=? WHERE provider=? AND repository=? AND number=?`, oldUntil, q.Provider, q.Repository, q.Number)
		return Outcome{}, err
	}
	rows, err := tx.Q().QueryContext(ctx, `SELECT r.issue_id,r.delivery_id,COALESCE(json_extract(o.observation_json,'$.attempted_at'),0),COALESCE(o.refresh_requested,0),COALESCE(o.server_refresh_revision,0) FROM server_delivery_references r LEFT JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id WHERE r.provider=? AND r.repository=? AND r.number=? AND (o.refresh_requested=1 OR `+serverDeliveryAutomaticMember+`) ORDER BY COALESCE(o.refresh_requested,0) DESC,COALESCE(json_extract(o.observation_json,'$.attempted_at'),0),r.issue_id,r.delivery_id LIMIT 100`, q.Provider, q.Repository, q.Number)
	if err != nil {
		return Outcome{}, err
	}
	type member struct {
		issue, delivery  string
		attempted        int64
		manual           bool
		requiredRevision int64
	}
	members := []member{}
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.issue, &m.delivery, &m.attempted, &m.manual, &m.requiredRevision); err != nil {
			break
		}
		members = append(members, m)
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil {
		return Outcome{}, err
	}
	if readErr != nil {
		return Outcome{}, readErr
	}
	var priorCache protocol.DeliveryObservation
	if err = json.Unmarshal([]byte(cache), &priorCache); err != nil {
		return Outcome{}, err
	}
	unseen, explicit := false, false
	for _, m := range members {
		unseen = unseen || m.attempted < priorCache.AttemptedAt
		explicit = explicit || (m.manual && m.requiredRevision > observedRefreshRevision)
	}
	if unseen && !explicit && priorCache.AttemptedAt > now-(5*time.Minute).Milliseconds() {
		members = slices.DeleteFunc(members, func(m member) bool { return !m.manual && m.attempted >= priorCache.AttemptedAt })
	}
	if len(members) == 0 {
		_, err = tx.Exec(ctx, `UPDATE server_delivery_jobs SET next_at=? WHERE provider=? AND repository=? AND number=?`, int64(9223372036854775807), q.Provider, q.Repository, q.Number)
		return Outcome{}, err
	}
	baseline, err := deliveryBaseline(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	q.ID = x.IDs.New(protocol.PrefixRequest)
	if _, err = tx.Exec(ctx, `INSERT INTO server_delivery_queries(query_id,provider,repository,number,status,started_at,refresh_revision) VALUES(?,?,?,?,'reading',?,?)`, q.ID, q.Provider, q.Repository, q.Number, now, refreshRevision); err != nil {
		return Outcome{}, err
	}
	manual, coveredRefresh := false, false
	var latestMemberAttempt int64
	for _, m := range members {
		row, err := observationRow(ctx, tx.Q(), m.issue, m.delivery)
		if err != nil {
			return Outcome{}, err
		}
		if err = retireRunnerDeliveryQuery(ctx, x, tx, &row); err != nil {
			return Outcome{}, err
		}
		manual = manual || (row.RefreshRequested && row.ServerRefreshRevision > observedRefreshRevision)
		coveredRefresh = coveredRefresh || row.RefreshRequested
		latestMemberAttempt = max(latestMemberAttempt, row.Observation.AttemptedAt)
		row.Observation.Generation++
		row.Observation.Observer = "server"
		row.Observation.BaselineUpgradeID = baseline.View.UpgradeID
		row.Observation.BaselineUpgradeRevision = baseline.View.Revision
		row.Nonce = q.ID
		row.HostID = "server"
		row.InstanceID = "server"
		row.QueryUntil = now + serverDeliveryLease.Milliseconds()
		row.Observation.RefreshPending = true
		if err = tx.PutDeliveryObservation(ctx, row); err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO server_delivery_members(query_id,issue_id,delivery_id,generation) VALUES(?,?,?,?)`, q.ID, m.issue, m.delivery, row.Observation.Generation); err != nil {
			return Outcome{}, err
		}
		trigger := x.IDs.New(protocol.Prefix("trigger"))
		if err = tx.InsertTrigger(ctx, protocol.TriggerEvent{ID: trigger, IssueID: m.issue, ScheduleID: "observation:" + m.issue + ":" + m.delivery, Title: "Delivery observation", Source: "observation", Status: "processing", RequestID: x.Env.RequestID, PlannedAt: now, CreatedAt: now, UpdatedAt: now, Steps: []protocol.TriggerStep{{Kind: "claimed", At: now, Detail: "server"}}}); err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO observation_jobs(issue_id,delivery_id,status,event_id,due_at,updated_at) VALUES(?,?,'claimed',?,?,?) ON CONFLICT(issue_id,delivery_id) DO UPDATE SET status='claimed',event_id=excluded.event_id,due_at=excluded.due_at,updated_at=excluded.updated_at`, m.issue, m.delivery, trigger, now, now); err != nil {
			return Outcome{}, err
		}
	}
	previousAttempts := attempts
	if attempts >= 3 {
		attempts = 0
	}
	attempts++
	if _, err = tx.Exec(ctx, `UPDATE server_delivery_jobs SET nonce=?,query_until=?,next_at=?,attempts=? WHERE provider=? AND repository=? AND number=?`, q.ID, now+serverDeliveryLease.Milliseconds(), now+serverDeliveryLease.Milliseconds(), attempts, q.Provider, q.Repository, q.Number); err != nil {
		return Outcome{}, err
	}
	var prior protocol.DeliveryObservation
	if err = json.Unmarshal([]byte(cache), &prior); err != nil {
		return Outcome{}, err
	}
	// Fresh shared facts also serve newly associated Issues, without a second
	// external query. Refreshes queued before that read share its result;
	// requests queued after it began require another bounded external read.
	if !manual && (coveredRefresh || latestMemberAttempt < prior.AttemptedAt) && prior.AttemptedAt > now-(5*time.Minute).Milliseconds() && prior.AttemptedAt > 0 && prior.BaselineUpgradeID == baseline.View.UpgradeID && prior.BaselineUpgradeRevision == baseline.View.Revision {
		var snapshot *protocol.DeliverySnapshot
		if prior.ErrorKind == "" || (prior.State == "merged" && prior.LineageErrorKind != "") {
			snapshot = &prior.DeliverySnapshot
		}
		if _, err = tx.Exec(ctx, `UPDATE server_delivery_queries SET refresh_revision=? WHERE query_id=?`, observedRefreshRevision, q.ID); err != nil {
			return Outcome{}, err
		}
		out, err := finishServerDelivery(ctx, x, tx, q.ID, snapshot, prior.ErrorKind, &prior)
		if err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE server_delivery_jobs SET attempts=? WHERE provider=? AND repository=? AND number=?`, previousAttempts, q.Provider, q.Repository, q.Number); err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE server_delivery_queries SET status='cached' WHERE query_id=?`, q.ID); err != nil {
			return Outcome{}, err
		}
		return out, nil
	}
	return Outcome{Result: &q}, nil
}

func (a *App) RecordServerDelivery(ctx context.Context, id string, snapshot *protocol.DeliverySnapshot, kind string) error {
	payload, err := json.Marshal(struct {
		ID       string                     `json:"id"`
		Snapshot *protocol.DeliverySnapshot `json:"snapshot,omitempty"`
		Error    string                     `json:"error,omitempty"`
	}{id, snapshot, kind})
	if err != nil {
		return err
	}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: id, Operation: "server.deliveries_report", Target: protocol.Target{Type: "delivery_observer"}, Payload: payload, Reason: "Persist the original shared provider read outcome."}, reportServerDelivery{ID: id, Snapshot: snapshot, Error: kind})
	if err != nil {
		return err
	}
	if op.Error != nil {
		return op.Error
	}
	return nil
}

type reportServerDelivery struct {
	ID       string
	Snapshot *protocol.DeliverySnapshot
	Error    string
}

func (reportServerDelivery) Operation() string { return "server.deliveries_report" }
func (reportServerDelivery) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c reportServerDelivery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	return finishServerDelivery(ctx, x, tx, c.ID, c.Snapshot, c.Error, nil)
}

func retireRunnerDeliveryQuery(ctx context.Context, x *Exec, tx *store.Tx, row *store.DeliveryObservationRow) error {
	if row.Nonce == "" || row.HostID == "server" {
		return nil
	}
	var event string
	err := tx.Q().QueryRowContext(ctx, `SELECT event_id FROM observation_jobs WHERE issue_id=? AND delivery_id=?`, row.Observation.WorkItemID, row.Observation.DeliveryID).Scan(&event)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO retired_runner_delivery_queries(nonce,issue_id,delivery_id,generation,host_id,instance_id,query_until,retired_at,event_id) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(nonce) DO NOTHING`, row.Nonce, row.Observation.WorkItemID, row.Observation.DeliveryID, row.Observation.Generation, row.HostID, row.InstanceID, row.QueryUntil, x.Now.UnixMilli(), event)
	if err != nil {
		return err
	}
	if event != "" {
		return tx.FinishTrigger(ctx, event, "failed", "Runner read retired after its bounded handoff; original result remains unknown until its original report arrives", x.Now.UnixMilli())
	}
	return nil
}
func retainRetiredDeliveryReport(ctx context.Context, x *Exec, tx *store.Tx, p protocol.DeliveryReportPayload) (bool, error) {
	var found bool
	err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM retired_runner_delivery_queries WHERE nonce=? AND issue_id=? AND delivery_id=? AND generation=? AND host_id=? AND instance_id=?)`, p.Nonce, p.WorkItemID, p.DeliveryID, p.Generation, x.Principal.Subject, p.InstanceID).Scan(&found)
	if err != nil || !found {
		return found, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE retired_runner_delivery_queries SET late_report_json=? WHERE nonce=? AND late_report_json=''`, string(raw), p.Nonce)
	return true, err
}

func finishServerDelivery(ctx context.Context, x *Exec, tx *store.Tx, id string, snapshot *protocol.DeliverySnapshot, kind string, cached *protocol.DeliveryObservation) (Outcome, error) {
	var provider, repo, nonce, cache string
	var number int64
	var attempts int
	var refreshRevision int64
	err := tx.Q().QueryRowContext(ctx, `SELECT j.provider,j.repository,j.number,j.nonce,j.attempts,j.observation_json,q.refresh_revision FROM server_delivery_jobs j JOIN server_delivery_queries q ON q.provider=j.provider AND q.repository=j.repository AND q.number=j.number WHERE q.query_id=?`, id).Scan(&provider, &repo, &number, &nonce, &attempts, &cache, &refreshRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return Outcome{NoEffect: true}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	raw, err := json.Marshal(struct {
		Snapshot *protocol.DeliverySnapshot `json:"snapshot,omitempty"`
		Error    string                     `json:"error,omitempty"`
	}{snapshot, kind})
	if err != nil {
		return Outcome{}, err
	}
	if nonce != id {
		_, err = tx.Exec(ctx, `UPDATE server_delivery_queries SET late_result_json=? WHERE query_id=? AND late_result_json='' `, string(raw), id)
		return Outcome{}, err
	}
	if (snapshot == nil) == (kind == "") && cached == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "provider facts or a read error required")
	}
	if snapshot != nil && !oneOf(snapshot.State, "open", "merged", "closed") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown provider state")
	}
	// A partial lineage read retains the provider's known merge facts while
	// consuming the same finite failure budget as a complete read timeout.
	if snapshot != nil && snapshot.State == "merged" && snapshot.LineageErrorKind != "" {
		kind = snapshot.LineageErrorKind
	}
	now := x.Now.UnixMilli()
	next := x.Now.Add(5 * time.Minute).UnixMilli()
	if snapshot != nil && snapshot.State == "open" && snapshot.CI != nil && oneOf(snapshot.CI.State, "pending", "running") {
		next = x.Now.Add(30 * time.Second).UnixMilli()
	}
	retryAt := int64(0)
	if kind != "" {
		next = x.Now.Add(15 * time.Minute).UnixMilli()
		if attempts < 3 && oneOf(kind, "timeout", "rate_limited", "unreachable", "query_failed") {
			retryAt = x.Now.Add(time.Duration(attempts) * 15 * time.Second).UnixMilli()
			next = retryAt
		}
	}
	var shared protocol.DeliveryObservation
	if err = json.Unmarshal([]byte(cache), &shared); err != nil {
		return Outcome{}, err
	}
	shared.AttemptedAt = now
	shared.ErrorKind = kind
	shared.ErrorMessage = deliveryObservationError(kind)
	if kind != "" && attempts >= 3 && oneOf(kind, "timeout", "rate_limited", "unreachable", "query_failed") {
		shared.ErrorMessage += " The retry budget is exhausted; repair the provider and explicitly refresh to retry."
	}
	shared.Observer = "server"
	shared.RetryCount = max(0, attempts-1)
	shared.RetryAt = retryAt
	if snapshot != nil {
		shared.DeliverySnapshot = *snapshot
		shared.CheckedAt = now
	}
	if cached != nil {
		shared = *cached
		next = cached.AttemptedAt + (5 * time.Minute).Milliseconds()
		if cached.RetryAt > now {
			next = cached.RetryAt
		}
	}
	rows, err := tx.Q().QueryContext(ctx, `SELECT issue_id,delivery_id,generation FROM server_delivery_members WHERE query_id=? ORDER BY issue_id,delivery_id`, id)
	if err != nil {
		return Outcome{}, err
	}
	type member struct {
		issue, delivery string
		generation      int64
	}
	members := []member{}
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.issue, &m.delivery, &m.generation); err != nil {
			break
		}
		members = append(members, m)
	}
	readErr := rows.Err()
	rows.Close()
	if err != nil {
		return Outcome{}, err
	}
	if readErr != nil {
		return Outcome{}, readErr
	}
	events := []Event{}
	for _, m := range members {
		row, err := observationRow(ctx, tx.Q(), m.issue, m.delivery)
		if err != nil {
			return Outcome{}, err
		}
		if row.Nonce != id || row.Observation.Generation != m.generation {
			continue
		}
		item, err := store.GetIssue(ctx, tx.Q(), m.issue)
		if err != nil {
			return Outcome{}, err
		}
		if d, ok := findDelivery(item, m.delivery); !ok || string(d.Provider) != provider || d.Repository != repo || d.Number != number {
			continue
		}
		previous := row.Observation
		manual := row.RefreshRequested
		baselineID, baselineRev := row.Observation.BaselineUpgradeID, row.Observation.BaselineUpgradeRevision
		row.Observation = shared
		if snapshot == nil {
			row.Observation.DeliverySnapshot = previous.DeliverySnapshot
			row.Observation.CheckedAt = previous.CheckedAt
		}
		row.Observation.WorkItemID = m.issue
		row.Observation.DeliveryID = m.delivery
		row.Observation.Generation = m.generation
		row.Observation.BaselineUpgradeID = baselineID
		row.Observation.BaselineUpgradeRevision = baselineRev
		shared.BaselineUpgradeID = baselineID
		shared.BaselineUpgradeRevision = baselineRev
		row.Nonce = ""
		row.QueryUntil = 0
		row.RefreshRequested = false
		row.Observation.RefreshPending = false
		row.NextCheckAt = next
		if err = tx.PutDeliveryObservation(ctx, row); err != nil {
			return Outcome{}, err
		}
		var trigger string
		if err = tx.Q().QueryRowContext(ctx, `SELECT event_id FROM observation_jobs WHERE issue_id=? AND delivery_id=?`, m.issue, m.delivery).Scan(&trigger); err != nil {
			return Outcome{}, err
		}
		status, reason := "completed", "server observation changed"
		if kind != "" {
			status, reason = "failed", kind
		} else if reflect.DeepEqual(previous.DeliverySnapshot, row.Observation.DeliverySnapshot) {
			status, reason = "not_dispatched", "observation unchanged"
		}
		if err = tx.FinishTrigger(ctx, trigger, status, reason, now); err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE observation_jobs SET status='finished',updated_at=? WHERE issue_id=? AND delivery_id=?`, now, m.issue, m.delivery); err != nil {
			return Outcome{}, err
		}
		event := deliveryChanged(item, row.Observation)
		event.Body["previous_state"] = previous.State
		event.Body["previous_error_kind"] = previous.ErrorKind
		if !manual && !(previous.RetryAt > 0 && row.Observation.RetryAt == 0) && reflect.DeepEqual(previous.DeliverySnapshot, row.Observation.DeliverySnapshot) && previous.ErrorKind == kind {
			event.Kind = "work_item.delivery_checked"
		}
		events = append(events, event)
	}
	// A bounded fanout can continue from the same shared cache for larger groups.
	var more bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_delivery_references r LEFT JOIN delivery_observations o ON o.work_item_id=r.issue_id AND o.delivery_id=r.delivery_id WHERE r.provider=? AND r.repository=? AND r.number=? AND (o.refresh_requested=1 OR (`+serverDeliveryAutomaticMember+` AND COALESCE(json_extract(o.observation_json,'$.attempted_at'),0)<?)))`, provider, repo, number, shared.AttemptedAt).Scan(&more); err != nil {
		return Outcome{}, err
	}
	if shared.State == "merged" && shared.RetryAt == 0 {
		next = int64(9223372036854775807)
	}
	if more {
		next = now
	}
	if kind == "" {
		attempts = 0
	}
	encoded, err := json.Marshal(shared)
	if err != nil {
		return Outcome{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE server_delivery_jobs SET nonce='',query_until=0,next_at=?,attempts=?,observation_json=?,observed_refresh_revision=? WHERE provider=? AND repository=? AND number=? AND nonce=?`, next, attempts, string(encoded), refreshRevision, provider, repo, number, id); err != nil {
		return Outcome{}, err
	}
	status := "completed"
	if kind != "" {
		status = "failed"
	}
	if _, err = tx.Exec(ctx, `UPDATE server_delivery_queries SET status=?,finished_at=?,result_json=? WHERE query_id=?`, status, now, string(raw), id); err != nil {
		return Outcome{}, err
	}
	return Outcome{Events: events}, nil
}

func deliveryObservationError(kind string) string {
	switch kind {
	case "":
		return ""
	case "provider_not_configured":
		return "Configure this provider and its native CLI on the server, then refresh."
	case "cli_unavailable":
		return "The server provider CLI is unavailable; repair its configured executable, then refresh."
	case "authentication_required":
		return "The server native provider login requires authentication; restore that login, then refresh."
	case "rate_limited":
		return "The provider returned a rate limit; bounded retry is scheduled when its budget remains."
	case "timeout":
		return "The provider read reached its deadline; bounded retry is scheduled when its budget remains."
	case "unreachable":
		return "The provider is unreachable; bounded retry is scheduled when its budget remains."
	default:
		return "The provider read failed; inspect the provider configuration and refresh after repair."
	}
}

// WithServerDeliveryObserverStatus records safe startup diagnosis. Native paths
// and credentials stay on the server; reads do not execute provider CLIs.
func WithServerDeliveryObserverStatus(status protocol.DeliveryObserverStatus) Option {
	return func(a *App) {
		a.serverDeliveryObservations = true
		status.Providers = slices.Clone(status.Providers)
		status.Executables = maps.Clone(status.Executables)
		a.deliveryObserverStatus = &status
	}
}
func (a *App) ServerDeliveryObserverStatus() *protocol.DeliveryObserverStatus {
	if a.deliveryObserverStatus == nil {
		return nil
	}
	status := *a.deliveryObserverStatus
	status.Providers = slices.Clone(status.Providers)
	status.Executables = maps.Clone(status.Executables)
	return &status
}
