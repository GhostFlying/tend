package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const concurrencyRetention = 7 * 24 * time.Hour

func concurrencyState(ctx context.Context, q store.Querier, id string) (domain.ConcurrencyState, error) {
	var s domain.ConcurrencyState
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state_json FROM concurrency_groups WHERE group_id=? AND deleted=0`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return s, protocol.Errorf(protocol.CodeNotFound, "concurrency group not found")
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal([]byte(raw), &s)
	return s, err
}
func saveConcurrency(ctx context.Context, tx *store.Tx, s domain.ConcurrencyState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO concurrency_groups(group_id,revision,state_json) VALUES(?,?,?) ON CONFLICT(group_id) DO UPDATE SET revision=excluded.revision,state_json=excluded.state_json`, s.Group.ID, s.Group.Revision, string(raw))
	return err
}
func concurrencyIDs(ctx context.Context, q store.Querier) ([]string, error) {
	return store.FollowupIDs(ctx, q, `SELECT group_id FROM concurrency_groups WHERE deleted=0 ORDER BY group_id`)
}
func concurrencyBinding(ctx context.Context, q store.Querier, env, runtime string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT group_id FROM concurrency_bindings WHERE environment_id=? AND runtime_id=?`, env, runtime).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func concurrencyBindings(ctx context.Context, q store.Querier, id string) ([]protocol.ConcurrencyBinding, error) {
	rows, err := q.QueryContext(ctx, `SELECT environment_id,runtime_id,group_id FROM concurrency_bindings WHERE group_id=? ORDER BY environment_id,runtime_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.ConcurrencyBinding{}
	for rows.Next() {
		var b protocol.ConcurrencyBinding
		if err := rows.Scan(&b.EnvironmentID, &b.RuntimeID, &b.GroupID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func concurrencyChange(ctx context.Context, tx *store.Tx, s domain.ConcurrencyState, before int64, reason string, now int64, actor, request string, changed ...protocol.ConcurrencyBinding) (int64, error) {
	g := s.Group
	c := protocol.ConcurrencyChange{GroupID: g.ID, GroupName: g.Name, At: now, Reason: reason, Before: before, After: g.Current, Min: g.Min, Initial: g.Initial, Max: g.Max, Occupied: g.Occupied, CooldownUntil: g.CooldownUntil, WindowStart: g.ObservationSince, WindowEnd: now, SaturatedMillis: g.SaturatedMillis, SuccessfulTurns: g.SuccessfulTurns, FeedbackHealthy: g.FeedbackHealthy, Waiting: g.Waiting, Actor: actor, RequestID: request, Sources: []protocol.ConcurrencySource{}}
	bindings, err := concurrencyBindings(ctx, tx.Q(), g.ID)
	if err != nil {
		return 0, err
	}
	c.Bindings = bindings
	for _, b := range changed {
		if b.EnvironmentID != "" && b.RuntimeID != "" {
			found := false
			for _, existing := range c.Bindings {
				if existing == b {
					found = true
				}
			}
			if !found {
				c.Bindings = append(c.Bindings, b)
			}
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(ctx, `INSERT INTO concurrency_changes(group_id,at,reason,snapshot_json) VALUES(?,?,?,?)`, g.ID, now, reason, string(raw))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ConfigureConcurrency is owner-only management. Adaptive updates do not change Revision.
type ConfigureConcurrency struct{ Action string }

func (c ConfigureConcurrency) Operation() string { return "concurrency." + c.Action }
func (c ConfigureConcurrency) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c ConfigureConcurrency) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	id := x.Env.Target.ID
	if x.Env.Target.Type != "concurrency" || !protocol.AgentInventoryID(id) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "valid concurrency group target required")
	}
	var p protocol.ConfigureConcurrencyPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	s, err := concurrencyState(ctx, tx.Q(), id)
	missing := protocol.CodeOf(err) == protocol.CodeNotFound
	if err != nil && !missing {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "expected_control_revision is required")
	}
	if *x.Env.ExpectedControlRevision != s.Group.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "concurrency configuration changed")
	}
	now := x.Now.UnixMilli()
	before := s.Group.Current
	if c.Action == "create" {
		if !missing {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "group already exists")
		}
		var exists bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM concurrency_groups WHERE group_id=?)`, id).Scan(&exists); err != nil {
			return Outcome{}, err
		}
		if exists {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "retired group ID cannot be reused")
		}
		s.Group.ID = id
	} else if missing {
		return Outcome{}, err
	}
	reason := "configuration"
	oldGroup := ""
	switch c.Action {
	case "create", "update":
		if strings.TrimSpace(p.Name) == "" || len(p.Name) > 200 || p.Min < 1 || p.Min > p.Initial || p.Initial > p.Max {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "name and 1 <= min <= initial <= max are required")
		}
		s.Group.Name, s.Group.Min, s.Group.Initial, s.Group.Max = p.Name, p.Min, p.Initial, p.Max
		if c.Action == "create" {
			s.Group.Current = p.Initial
			reason = "initial"
		} else {
			s.Group.Current = max(p.Min, min(p.Max, s.Group.Current))
		}
	case "bind", "unbind":
		if !protocol.AgentInventoryID(p.EnvironmentID) || !protocol.AgentInventoryID(p.RuntimeID) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "environment_id and runtime_id are required")
		}
		if c.Action == "bind" {
			if err := validateConcurrencyRuntime(ctx, tx.Q(), p.EnvironmentID, p.RuntimeID); err != nil {
				return Outcome{}, err
			}
		}
		old, err := concurrencyBinding(ctx, tx.Q(), p.EnvironmentID, p.RuntimeID)
		if err != nil {
			return Outcome{}, err
		}
		if c.Action == "unbind" && old != id {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "runtime is not bound to this group")
		}
		if old != "" && old != id {
			oldGroup = old
			previous, err := concurrencyState(ctx, tx.Q(), old)
			if err != nil {
				return Outcome{}, err
			}
			previous.Group.Revision++
			previous.Reset(max(now, previous.Group.CooldownUntil))
			previous.LastSample = now
			if err := saveConcurrency(ctx, tx, previous); err != nil {
				return Outcome{}, err
			}
			if _, err := concurrencyChange(ctx, tx, previous, previous.Group.Current, "unbind", now, x.Principal.Subject, x.Env.RequestID); err != nil {
				return Outcome{}, err
			}
		}
		if c.Action == "bind" {
			_, err = tx.Exec(ctx, `INSERT INTO concurrency_bindings(environment_id,runtime_id,group_id) VALUES(?,?,?) ON CONFLICT(environment_id,runtime_id) DO UPDATE SET group_id=excluded.group_id`, p.EnvironmentID, p.RuntimeID, id)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM concurrency_bindings WHERE environment_id=? AND runtime_id=? AND group_id=?`, p.EnvironmentID, p.RuntimeID, id)
		}
		if err != nil {
			return Outcome{}, err
		}
		reason = c.Action
	case "delete":
		bindings, err := concurrencyBindings(ctx, tx.Q(), id)
		if err != nil {
			return Outcome{}, err
		}
		if len(bindings) > 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "unbind runtimes before deleting a group")
		}
		_, err = tx.Exec(ctx, `UPDATE concurrency_groups SET deleted=1,revision=revision+1 WHERE group_id=?`, id)
		return Outcome{Notify: true, Result: map[string]any{"id": id, "deleted": true}}, err
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown concurrency operation")
	}
	s.Group.Revision++
	s.Reset(max(now, s.Group.CooldownUntil))
	s.LastSample = now
	if err := saveConcurrency(ctx, tx, s); err != nil {
		return Outcome{}, err
	}
	if _, err := concurrencyChange(ctx, tx, s, before, reason, now, x.Principal.Subject, x.Env.RequestID, protocol.ConcurrencyBinding{EnvironmentID: p.EnvironmentID, RuntimeID: p.RuntimeID, GroupID: id}); err != nil {
		return Outcome{}, err
	}
	if err := syncConcurrencyExecutions(ctx, tx, now); err != nil {
		return Outcome{}, err
	}
	if oldGroup != "" {
		previous, err := concurrencyState(ctx, tx.Q(), oldGroup)
		if err != nil {
			return Outcome{}, err
		}
		if err := refreshConcurrencyLoad(ctx, tx, &previous, now); err != nil {
			return Outcome{}, err
		}
		if err := saveConcurrency(ctx, tx, previous); err != nil {
			return Outcome{}, err
		}
	}
	if err := refreshConcurrencyLoad(ctx, tx, &s, now); err != nil {
		return Outcome{}, err
	}
	if err := saveConcurrency(ctx, tx, s); err != nil {
		return Outcome{}, err
	}
	return Outcome{Notify: true, Result: s.Group, Events: []Event{{ObjectType: "concurrency", ObjectID: id, ObjectVersion: s.Group.Revision, Kind: "concurrency.configured", Body: map[string]any{"action": c.Action}}}}, nil
}
func validateConcurrencyRuntime(ctx context.Context, q store.Querier, env, runtime string) error {
	var retained bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM concurrency_executions WHERE environment_id=? AND runtime_id=?)`, env, runtime).Scan(&retained); err != nil {
		return err
	}
	if retained {
		return nil
	}
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_agent_executions WHERE json_extract(pin_json,'$.identity.execution_environment_id')=? AND json_extract(pin_json,'$.runtime.runtime_id')=?) OR EXISTS(SELECT 1 FROM hosted_executions WHERE json_extract(private_snapshot,'$.identity.execution_environment_id')=? AND json_extract(private_snapshot,'$.runtime.runtime_id')=?)`, env, runtime, env, runtime).Scan(&retained); err != nil {
		return err
	}
	if retained {
		return nil
	}

	var raw string
	err := q.QueryRowContext(ctx, `SELECT public_json FROM hosted_configuration_versions WHERE environment_id=? ORDER BY revision DESC LIMIT 1`, env).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		inv, e := store.GetAgentInventory(ctx, q, env)
		if e != nil {
			return errNotFound(e, "runtime environment not found")
		}
		for _, r := range inv.Projection.Runtimes {
			if r.RuntimeID == runtime {
				return nil
			}
		}
	} else if err != nil {
		return err
	} else {
		var p protocol.AgentInventoryProjection
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		for _, r := range p.Runtimes {
			if r.RuntimeID == runtime {
				return nil
			}
		}
	}
	return protocol.Errorf(protocol.CodeNotFound, "registered runtime not found")
}
func normalizeConcurrencyQuery(q protocol.ConcurrencyQuery) (protocol.ConcurrencyQuery, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 || q.Since < 0 || q.Until < 0 {
		return q, protocol.Errorf(protocol.CodeInvalidRequest, "invalid concurrency query")
	}
	return q, nil
}
func (a *App) ConcurrencyGroups(ctx context.Context, query protocol.ConcurrencyQuery) (protocol.ConcurrencyGroupPage, error) {
	query, err := normalizeConcurrencyQuery(query)
	if query.IssueID != "" || query.SessionID != "" || query.Reason != "" || query.Since != 0 || query.Until != 0 {
		return protocol.ConcurrencyGroupPage{}, protocol.Errorf(protocol.CodeInvalidRequest, "Issue, session, cause and time filters apply to history only")
	}
	page := protocol.ConcurrencyGroupPage{Items: []protocol.ConcurrencyGroup{}}
	if err != nil {
		return page, err
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := concurrencyIDs(ctx, tx.Q())
		if err != nil {
			return err
		}
		for _, id := range ids {
			if id <= query.After || query.GroupID != "" && id != query.GroupID {
				continue
			}
			s, err := concurrencyState(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			s.Group.Bindings, err = concurrencyBindings(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			if query.Search != "" && !strings.Contains(strings.ToLower(s.Group.Name+" "+id), strings.ToLower(query.Search)) {
				continue
			}
			if query.EnvironmentID != "" || query.RuntimeID != "" {
				found := false
				for _, b := range s.Group.Bindings {
					if (query.EnvironmentID == "" || b.EnvironmentID == query.EnvironmentID) && (query.RuntimeID == "" || b.RuntimeID == query.RuntimeID) {
						found = true
					}
				}
				if !found {
					continue
				}
			}
			if len(page.Items) == query.Limit {
				page.More = true
				page.Next = page.Items[len(page.Items)-1].ID
				break
			}
			page.Items = append(page.Items, s.Group)
		}
		return nil
	})
	return page, err
}
func (a *App) ConcurrencyGroup(ctx context.Context, id string) (protocol.ConcurrencyGroup, error) {
	var g protocol.ConcurrencyGroup
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		s, err := concurrencyState(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		g = s.Group
		g.Bindings, err = concurrencyBindings(ctx, tx.Q(), id)
		return err
	})
	return g, err
}
func (a *App) ConcurrencyHistory(ctx context.Context, query protocol.ConcurrencyQuery) (protocol.ConcurrencyHistoryPage, error) {
	q, err := normalizeConcurrencyQuery(query)
	now := a.now().UnixMilli()
	page := protocol.ConcurrencyHistoryPage{Items: []protocol.ConcurrencyChange{}, Since: max(q.Since, now-concurrencyRetention.Milliseconds()), Until: now}
	if q.Until != 0 {
		page.Until = min(now, q.Until)
	}
	if err != nil {
		return page, err
	}
	if page.Since > page.Until {
		return page, protocol.Errorf(protocol.CodeInvalidRequest, "time range is outside retained history")
	}
	cursor := int64(0)
	if q.After != "" {
		cursor, err = strconv.ParseInt(q.After, 10, 64)
		if err != nil || cursor < 1 {
			return page, protocol.Errorf(protocol.CodeInvalidRequest, "invalid history cursor")
		}
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		statement := `SELECT c.change_id,c.snapshot_json FROM concurrency_changes c WHERE c.at>=? AND c.at<=? AND (?='' OR c.group_id=?) AND (?='' OR c.reason=?) AND (?=0 OR (c.at,c.change_id)<(SELECT at,change_id FROM concurrency_changes WHERE change_id=?))`
		args := []any{page.Since, page.Until, q.GroupID, q.GroupID, q.Reason, q.Reason, cursor, cursor}
		if q.IssueID != "" || q.SessionID != "" {
			statement += ` AND EXISTS(SELECT 1 FROM concurrency_feedback f WHERE f.change_id=c.change_id AND (?='' OR json_extract(f.source_json,'$.issue_id')=?) AND (?='' OR json_extract(f.source_json,'$.session_id')=? OR json_extract(f.source_json,'$.native_session_id')=?) AND (?='' OR json_extract(f.source_json,'$.environment_id')=?) AND (?='' OR json_extract(f.source_json,'$.runtime_id')=?))`
			args = append(args, q.IssueID, q.IssueID, q.SessionID, q.SessionID, q.SessionID, q.EnvironmentID, q.EnvironmentID, q.RuntimeID, q.RuntimeID)
		} else if q.EnvironmentID != "" || q.RuntimeID != "" {
			statement += ` AND (EXISTS(SELECT 1 FROM json_each(c.snapshot_json,'$.bindings') b WHERE (?='' OR json_extract(b.value,'$.environment_id')=?) AND (?='' OR json_extract(b.value,'$.runtime_id')=?)) OR EXISTS(SELECT 1 FROM concurrency_feedback f WHERE f.change_id=c.change_id AND (?='' OR json_extract(f.source_json,'$.environment_id')=?) AND (?='' OR json_extract(f.source_json,'$.runtime_id')=?)))`
			args = append(args, q.EnvironmentID, q.EnvironmentID, q.RuntimeID, q.RuntimeID, q.EnvironmentID, q.EnvironmentID, q.RuntimeID, q.RuntimeID)
		}
		if q.Search != "" {
			statement += ` AND (instr(lower(c.snapshot_json),lower(?))>0 OR EXISTS(SELECT 1 FROM concurrency_feedback f WHERE f.change_id=c.change_id AND instr(lower(f.source_json),lower(?))>0))`
			args = append(args, q.Search, q.Search)
		}
		statement += ` ORDER BY c.at DESC,c.change_id DESC LIMIT ?`
		args = append(args, q.Limit+1)
		rows, err := tx.Q().QueryContext(ctx, statement, args...)
		if err != nil {
			return err
		}
		type rawChange struct {
			id  int64
			raw string
		}
		var raw []rawChange
		for rows.Next() {
			var v rawChange
			if err := rows.Scan(&v.id, &v.raw); err != nil {
				rows.Close()
				return err
			}
			raw = append(raw, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, v := range raw {
			var c protocol.ConcurrencyChange
			if err := json.Unmarshal([]byte(v.raw), &c); err != nil {
				return err
			}
			c.ID = v.id
			c.Sources = []protocol.ConcurrencySource{}
			sourceRows, err := tx.Q().QueryContext(ctx, `SELECT source_json FROM concurrency_feedback WHERE change_id=? ORDER BY received_at,feedback_id`, c.ID)
			if err != nil {
				return err
			}
			for sourceRows.Next() {
				var text string
				if err := sourceRows.Scan(&text); err != nil {
					sourceRows.Close()
					return err
				}
				var source protocol.ConcurrencySource
				if err := json.Unmarshal([]byte(text), &source); err != nil {
					sourceRows.Close()
					return err
				}
				c.Sources = append(c.Sources, source)
			}
			if c.Reason == "congestion" || c.Reason == "minimum_cooldown" {
				c.CongestionCount = int64(len(c.Sources))
			}
			err = sourceRows.Err()
			sourceRows.Close()
			if err != nil {
				return err
			}

			if len(page.Items) == q.Limit {
				page.More = true
				page.Next = strconv.FormatInt(page.Items[len(page.Items)-1].ID, 10)
				break
			}
			page.Items = append(page.Items, c)
		}
		return nil
	})
	return page, err
}

func putConcurrencyExecution(ctx context.Context, tx *store.Tx, source protocol.ConcurrencySource, state string, now int64) error {
	return putConcurrencyExecutionObserved(ctx, tx, source, state, now, now)
}

// Observation time advances the controller; lastSeen only establishes liveness.
func putConcurrencyExecutionObserved(ctx context.Context, tx *store.Tx, source protocol.ConcurrencySource, state string, lastSeen, now int64) error {
	return putConcurrencyExecutionSampled(ctx, tx, source, state, lastSeen, now, nil)
}

// concurrencySamples reuses only observations of an unchanged execution set
// within one sync call. Any mutation invalidates every group because child
// relationships can change a parent's leaf load across group boundaries.
type concurrencySamples struct {
	seen map[string]bool
}

func (s *concurrencySamples) sample(ctx context.Context, tx *store.Tx, ids []string, now int64) error {
	if s == nil {
		return sampleConcurrencyGroups(ctx, tx, ids, now)
	}
	for _, id := range ids {
		if id == "" || s.seen[id] {
			continue
		}
		if err := sampleConcurrencyGroups(ctx, tx, []string{id}, now); err != nil {
			return err
		}
		s.seen[id] = true
	}
	return nil
}

func putConcurrencyExecutionSampled(ctx context.Context, tx *store.Tx, source protocol.ConcurrencySource, state string, lastSeen, now int64, samples *concurrencySamples) error {
	group, err := concurrencyBinding(ctx, tx.Q(), source.EnvironmentID, source.RuntimeID)
	if err != nil {
		return err
	}
	var previousRaw, previousGroup, previousState, previousParent string
	var previousSeen int64
	lookup := tx.Q().QueryRowContext(ctx, `SELECT source_json,group_id,state,last_seen,parent_execution_id FROM concurrency_executions WHERE execution_id=?`, source.ExecutionID).Scan(&previousRaw, &previousGroup, &previousState, &previousSeen, &previousParent)
	if lookup != nil && !errors.Is(lookup, sql.ErrNoRows) {
		return lookup
	}
	if lookup == nil {
		var previous protocol.ConcurrencySource
		if err := json.Unmarshal([]byte(previousRaw), &previous); err != nil {
			return err
		}
		if source.NativeSessionID == "" {
			source.NativeSessionID = previous.NativeSessionID
		}
		if source.EventID == "" {
			source.EventID = previous.EventID
		}
	}
	groups := []string{group, previousGroup}
	if err := samples.sample(ctx, tx, groups, now); err != nil {
		return err
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return err
	}
	effectiveGroup := group
	if state == "stopped" && lookup == nil {
		effectiveGroup = previousGroup
	}
	sameFacts := lookup == nil && previousRaw == string(raw) && previousState == state && previousGroup == effectiveGroup && previousParent == source.ParentExecutionID
	if samples != nil && sameFacts && previousSeen >= lastSeen {
		return nil
	}
	// A heartbeat still persists its new liveness, but cannot change leaf load
	// when every other fact and the health classification at this sync's now
	// are unchanged. Crossing freshness must retain the original pre/post sample.
	sameLoad := false
	if samples != nil && sameFacts {
		unhealthy := func(seen int64) bool {
			return state == "unknown" || state == "running" && source.NativeSessionID == "" || now-seen > RunnerOfflineAfter.Milliseconds()
		}
		sameLoad = state == "stopped" || unhealthy(previousSeen) == unhealthy(max(previousSeen, lastSeen))
	}
	_, err = tx.Exec(ctx, `INSERT INTO concurrency_executions(execution_id,environment_id,runtime_id,state,source_json,last_seen,group_id,parent_execution_id) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(execution_id) DO UPDATE SET state=excluded.state,source_json=excluded.source_json,last_seen=MAX(concurrency_executions.last_seen,excluded.last_seen),parent_execution_id=excluded.parent_execution_id,group_id=CASE WHEN excluded.state='stopped' THEN concurrency_executions.group_id ELSE excluded.group_id END`, source.ExecutionID, source.EnvironmentID, source.RuntimeID, state, string(raw), lastSeen, group, source.ParentExecutionID)
	if err != nil {
		return err
	}
	if samples != nil && !sameLoad {
		clear(samples.seen)
	}
	return samples.sample(ctx, tx, groups, now)
}
func concurrencyAvailable(ctx context.Context, q store.Querier, env, runtime, execution string, now int64) (bool, error) {
	id, err := concurrencyBinding(ctx, q, env, runtime)
	if err != nil || id == "" {
		return true, err
	}
	s, err := concurrencyState(ctx, q, id)
	if err != nil {
		return false, err
	}
	var held bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM concurrency_executions WHERE execution_id=? AND state<>'stopped')`, execution).Scan(&held); err != nil {
		return false, err
	}
	if held {
		return true, nil
	}
	var occupied int64
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM concurrency_executions e JOIN concurrency_bindings b USING(environment_id,runtime_id) WHERE b.group_id=? AND e.state<>'stopped' AND NOT EXISTS(SELECT 1 FROM concurrency_executions child WHERE child.parent_execution_id=e.execution_id AND child.state<>'stopped')`, id).Scan(&occupied)
	return now >= s.Group.CooldownUntil && occupied < s.Group.Current, err
}
func refreshConcurrencyLoad(ctx context.Context, tx *store.Tx, s *domain.ConcurrencyState, now int64) error {
	defer tx.ObservePhase(store.WriteConcurrencyLoad)()
	rows, err := tx.Q().QueryContext(ctx, `SELECT e.state,e.last_seen,COALESCE(json_extract(e.source_json,'$.native_session_id'),'') FROM concurrency_executions e JOIN concurrency_bindings b USING(environment_id,runtime_id) WHERE b.group_id=? AND e.state<>'stopped' AND NOT EXISTS(SELECT 1 FROM concurrency_executions child WHERE child.parent_execution_id=e.execution_id AND child.state<>'stopped')`, s.Group.ID)
	if err != nil {
		return err
	}
	occupied, running := int64(0), int64(0)
	healthy := true
	for rows.Next() {
		var state, native string
		var last int64
		if err := rows.Scan(&state, &last, &native); err != nil {
			rows.Close()
			return err
		}
		occupied++
		if state == "running" {
			running++
		}
		if state == "unknown" || (state == "running" && native == "") || now-last > RunnerOfflineAfter.Milliseconds() {
			healthy = false
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	s.Group.Bindings, err = concurrencyBindings(ctx, tx.Q(), s.Group.ID)
	if err != nil {
		return err
	}
	s.Sample(now, occupied, running, s.Group.Waiting, healthy)
	return nil
}

// RecordConcurrencyFeedback only consumes typed, identity-bound native facts.
func recordConcurrencyFeedback(ctx context.Context, tx *store.Tx, source protocol.ConcurrencySource, f protocol.ConcurrencyFeedback, now int64) error {
	if f.Kind != "success" && !domain.ConcurrencyCongestion(f.Failure) {
		return nil
	}
	id, err := concurrencyBinding(ctx, tx.Q(), source.EnvironmentID, source.RuntimeID)
	if err != nil {
		return err
	}
	var executionGroup string
	lookup := tx.Q().QueryRowContext(ctx, `SELECT group_id FROM concurrency_executions WHERE execution_id=?`, source.ExecutionID).Scan(&executionGroup)
	if lookup == nil {
		id = executionGroup
	} else if !errors.Is(lookup, sql.ErrNoRows) {
		return lookup
	}
	if id == "" {
		return nil
	}
	source.Kind = f.Kind
	source.NativeSessionID = f.SessionID
	source.TurnID = f.TurnID
	source.Attempt = f.Attempt
	source.OccurredAt = f.OccurredAt
	source.ReceivedAt = now
	if f.Kind != "success" {
		source.Kind = "congestion"
		source.Code = f.Failure.Code
		source.HTTPStatus = f.Failure.HTTPStatus
	}
	attemptKey := source.Attempt
	if source.TurnID != "" {
		attemptKey = "native-turn"
	}
	key := source.ExecutionID + "/" + attemptKey + "/" + source.NativeSessionID + "/" + source.TurnID + "/" + source.Kind
	if source.TurnID != "" {
		key = "native:" + source.EnvironmentID + "/" + source.RuntimeID + "/" + source.NativeSessionID + "/" + source.TurnID + "/" + source.Kind
	}
	source.EventID = key
	raw, err := json.Marshal(source)
	if err != nil {
		return err
	}
	res, err := tx.Exec(ctx, `INSERT OR IGNORE INTO concurrency_feedback(feedback_id,group_id,received_at,source_json) VALUES(?,?,?,?)`, key, id, now, string(raw))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	s, err := concurrencyState(ctx, tx.Q(), id)
	if protocol.CodeOf(err) == protocol.CodeNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if err := refreshConcurrencyLoad(ctx, tx, &s, now); err != nil {
		return err
	}
	if source.Kind == "success" {
		s.Successes = append(s.Successes, now)
		s.Group.SuccessfulTurns = int64(len(s.Successes))
	} else {
		before := s.Group.Current
		if s.Congested(now) {
			reason := "congestion"
			if before == s.Group.Current {
				reason = "minimum_cooldown"
			}
			change, err := concurrencyChange(ctx, tx, s, before, reason, now, "", "")
			if err != nil {
				return err
			}
			s.LastChangeID = change
		}
		if _, err := tx.Exec(ctx, `UPDATE concurrency_feedback SET change_id=? WHERE feedback_id=?`, s.LastChangeID, key); err != nil {
			return err
		}
	}
	return saveConcurrency(ctx, tx, s)
}

func (a *App) RunConcurrency(ctx context.Context, log *slog.Logger) {
	if err := a.store.Write(ctx, func(tx *store.Tx) error {
		ids, err := concurrencyIDs(ctx, tx.Q())
		if err != nil {
			return err
		}
		for _, id := range ids {
			s, err := concurrencyState(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			s.Reset(max(a.now().UnixMilli(), s.Group.CooldownUntil))
			s.LastSample = a.now().UnixMilli()
			if err := saveConcurrency(ctx, tx, s); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.ErrorContext(ctx, "reset concurrency observation", "err", err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.tickConcurrency(ctx); err != nil {
				log.ErrorContext(ctx, "update concurrency budgets", "err", err)
			}
		}
	}
}
func (a *App) tickConcurrency(ctx context.Context) error {
	now := a.now().UnixMilli()
	notify := false
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		var count int
		if err := tx.Q().QueryRowContext(ctx, `SELECT count(*) FROM concurrency_groups`).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		if err := syncConcurrencyExecutions(ctx, tx, now); err != nil {
			return err
		}
		if err := a.updateConcurrencyWaiting(ctx, tx); err != nil {
			return err
		}
		ids, err := concurrencyIDs(ctx, tx.Q())
		if err != nil {
			return err
		}
		for _, id := range ids {
			s, err := concurrencyState(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			previousOccupied, previousSample := s.Group.Occupied, s.LastSample
			if err := refreshConcurrencyLoad(ctx, tx, &s, now); err != nil {
				return err
			}
			if s.Group.Occupied < previousOccupied || previousSample < s.Group.CooldownUntil && now >= s.Group.CooldownUntil {
				notify = true
			}
			proof := s
			before := s.Group.Current
			if s.Increase(now) {
				notify = true
				proof.Group.Current = s.Group.Current
				if _, err := concurrencyChange(ctx, tx, proof, before, "stable_demand", now, "", ""); err != nil {
					return err
				}
			}
			if err := saveConcurrency(ctx, tx, s); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE concurrency_feedback SET source_json='{}' WHERE source_json<>'{}' AND received_at<?`, now-concurrencyRetention.Milliseconds()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM concurrency_changes WHERE at<?`, now-concurrencyRetention.Milliseconds())
		return err
	})
	if err == nil && notify {
		a.notifyChange()
	}
	return err
}

// Error keeps the ordinary dispatcher retry path; no native execution was sent.
var ErrConcurrencyCapacity = errors.New("concurrency group capacity unavailable")

func concurrencyEnvironmentBindings(ctx context.Context, q store.Querier, env string) ([]protocol.ConcurrencyBinding, error) {
	rows, err := q.QueryContext(ctx, `SELECT environment_id,runtime_id,group_id FROM concurrency_bindings WHERE environment_id=? ORDER BY runtime_id`, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []protocol.ConcurrencyBinding
	for rows.Next() {
		var b protocol.ConcurrencyBinding
		if err := rows.Scan(&b.EnvironmentID, &b.RuntimeID, &b.GroupID); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}

// Close each load interval at the actual ledger transition; the timer only
// advances open intervals and detects feedback gaps.
func sampleConcurrencyGroups(ctx context.Context, tx *store.Tx, ids []string, now int64) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		s, err := concurrencyState(ctx, tx.Q(), id)
		if protocol.CodeOf(err) == protocol.CodeNotFound {
			continue
		}
		if err != nil {
			return err
		}
		if err := refreshConcurrencyLoad(ctx, tx, &s, now); err != nil {
			return err
		}
		if err := saveConcurrency(ctx, tx, s); err != nil {
			return err
		}
	}
	return nil
}
