package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const usageCollectionRows = `SELECT source_key id,'' host_id,run_id,conversation_id,COALESCE(json_extract(source_json,'$.thread_id'),'') thread_id,COALESCE(json_extract(source_json,'$.session_id'),'') session_id,COALESCE(json_extract(source_json,'$.provider'),'') provider,coverage,writer_stopped,collection_json,json_extract(collection_json,'$.revision') control_revision,'[]' pending_requests,(SELECT COUNT(*) FROM json_each(cursors_json)) cursor_files,COALESCE((SELECT SUM(json_extract(value,'$.offset')) FROM json_each(cursors_json)),0) cursor_offset FROM token_usage_local_sources UNION ALL SELECT s.source_key,s.host_id,s.run_id,s.conversation_id,s.thread_id,s.session_id,s.provider,s.coverage,COALESCE(json_extract(r.observation_json,'$.writer_stopped'),0),COALESCE(r.collection_json,'{"state":"unobserved","epoch":0,"revision":0,"execution_evidence":"unknown"}'),COALESCE(r.control_revision,0),COALESCE(json_extract(r.observation_json,'$.pending_request_ids'),'[]'),COALESCE(json_extract(r.observation_json,'$.cursor_files'),0),COALESCE(json_extract(r.observation_json,'$.cursor_offset'),0) FROM token_usage_sources s LEFT JOIN token_usage_remote_collection r ON r.source_key=s.source_key WHERE s.host_id<>'' AND NOT EXISTS(SELECT 1 FROM token_usage_local_sources l WHERE l.run_id=s.run_id AND json_extract(l.source_json,'$.thread_id')=s.thread_id)`

func UsageCollections(ctx context.Context, q store.Querier, f protocol.UsageCollectionQuery, id string) (protocol.UsageCollectionPage, error) {
	out := protocol.UsageCollectionPage{Sources: []protocol.UsageCollectionSource{}}
	if f.Limit == 0 {
		f.Limit = 50
	}
	if f.Limit < 1 || f.Limit > 100 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be 1..100")
	}
	if f.State != "" && !slices.Contains([]string{protocol.UsageDormant, protocol.UsageActive, protocol.UsageSettling, protocol.UsageNeedsReconcile, protocol.UsageRetired, "unobserved"}, f.State) {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "unknown collection state")
	}
	if f.Reason != "" && !slices.Contains([]string{"complete", "empty", "unavailable"}, f.Reason) {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "unknown retirement reason")
	}
	rows, err := q.QueryContext(ctx, `SELECT * FROM (`+usageCollectionRows+`) WHERE (?='' OR id=?) AND id>? AND (?='' OR host_id=?) AND (?='' OR run_id=?) AND (?='' OR conversation_id=?) AND (?='' OR thread_id=?) AND (?='' OR json_extract(collection_json,'$.state')=?) AND (?='' OR json_extract(collection_json,'$.retirement_reason')=?) ORDER BY id LIMIT ?`, id, id, f.After, f.Host, f.Host, f.Run, f.Run, f.Conversation, f.Conversation, f.Thread, f.Thread, f.State, f.State, f.Reason, f.Reason, f.Limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var s protocol.UsageCollectionSource
		var raw, pending string
		if err = rows.Scan(&s.ID, &s.HostID, &s.RunID, &s.ConversationID, &s.ThreadID, &s.SessionID, &s.Provider, &s.Coverage, &s.WriterStopped, &raw, &s.ControlRevision, &pending, &s.CursorFiles, &s.CursorOffset); err != nil {
			_ = rows.Close()
			return out, err
		}
		if err = json.Unmarshal([]byte(raw), &s.Collection); err != nil {
			_ = rows.Close()
			return out, err
		}
		if err = json.Unmarshal([]byte(pending), &s.PendingRequestIDs); err != nil {
			_ = rows.Close()
			return out, err
		}
		s.Commands = []protocol.UsageCollectionCommandStatus{}
		if s.PendingRequestIDs == nil {
			s.PendingRequestIDs = []string{}
		}
		out.Sources = append(out.Sources, s)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	out.More = len(out.Sources) > f.Limit
	if out.More {
		out.Sources = out.Sources[:f.Limit]
		out.Next = out.Sources[len(out.Sources)-1].ID
	}
	for i := range out.Sources {
		if id == "" {
			continue
		}
		s := &out.Sources[i]
		r, e := q.QueryContext(ctx, `SELECT command_json,status FROM token_usage_collection_commands WHERE source_key=? ORDER BY created_at DESC,request_id DESC LIMIT 50`, s.ID)
		if e != nil {
			return out, e
		}
		for r.Next() {
			var raw, status string
			if e = r.Scan(&raw, &status); e != nil {
				_ = r.Close()
				return out, e
			}
			var c protocol.UsageCollectionCommandStatus
			if e = json.Unmarshal([]byte(raw), &c.UsageCollectionCommand); e != nil {
				_ = r.Close()
				return out, e
			}
			c.Status = status
			s.Commands = append(s.Commands, c)
		}
		e = r.Err()
		_ = r.Close()
		if e != nil {
			return out, e
		}
	}
	if id != "" && len(out.Sources) == 0 {
		return out, protocol.Errorf(protocol.CodeNotFound, "usage collection not found")
	}
	return out, nil
}

func usageCollectionEnabled(ctx context.Context, q store.Querier, host string) bool {
	runtime, err := store.GetHostRuntime(ctx, q, host)
	return err == nil && slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityUsageCollection)
}

type ControlUsageCollection struct{ Resume bool }

func (c ControlUsageCollection) Operation() string {
	if c.Resume {
		return protocol.OpResumeUsageCollection
	}
	return protocol.OpRetireUsageCollection
}
func (ControlUsageCollection) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c ControlUsageCollection) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "usage_source" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "usage source target required")
	}
	revision, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	var input protocol.UsageCollectionControl
	if err = decodePayload(x.Env, &input); err != nil {
		return Outcome{}, err
	}
	if !c.Resume && input.Reason != "unavailable" || c.Resume && input.Reason != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "only unavailable can be explicitly retired")
	}
	page, err := UsageCollections(ctx, tx.Q(), protocol.UsageCollectionQuery{}, x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	source := page.Sources[0]
	if source.ControlRevision != revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "usage source control changed")
	}
	if source.HostID == "" {
		s, err := localUsageSource(ctx, tx.Q(), source.ID)
		if err != nil {
			return Outcome{}, err
		}
		if c.Resume {
			s.Collection.Reopen("reconcile/" + x.Env.RequestID)
		} else {
			s.Collection.RetireUnavailable(x.Now.UnixMilli(), s.WriterStopped)
		}
		if err = saveLocalUsage(ctx, tx, s, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: s.Collection}, nil
	}
	if !usageCollectionEnabled(ctx, tx.Q(), source.HostID) || source.Collection.Epoch == 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host collection capability and observation required")
	}
	var pending bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_collection_commands WHERE source_key=? AND status='pending')`, source.ID).Scan(&pending); err != nil {
		return Outcome{}, err
	}
	if pending {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "original collection command is pending")
	}
	var localID string
	if err = tx.Q().QueryRowContext(ctx, `SELECT local_source_id FROM token_usage_remote_collection WHERE source_key=?`, source.ID).Scan(&localID); err != nil {
		return Outcome{}, err
	}
	command := protocol.UsageCollectionCommand{RequestID: x.Env.RequestID, SourceID: localID, RunID: source.RunID, Action: "retire", Reason: input.Reason, ExpectedEpoch: source.Collection.Epoch, ExpectedRevision: source.Collection.Revision}
	if c.Resume {
		command.Action = "resume"
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return Outcome{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO token_usage_collection_commands(request_id,source_key,host_id,command_json,status,created_at) VALUES(?,?,?,?,'pending',?)`, command.RequestID, source.ID, source.HostID, string(raw), x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	result, err := tx.Exec(ctx, `UPDATE token_usage_remote_collection SET control_revision=control_revision+1 WHERE source_key=?`, source.ID)
	if err = usageCollectionChanged(tx, result, err); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: protocol.UsageCollectionCommandStatus{UsageCollectionCommand: command, Status: "pending"}}, nil
}

type ObserveUsageCollection struct{}

func (ObserveUsageCollection) Operation() string { return protocol.OpRunnerUsageCollection }
func (ObserveUsageCollection) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRunner || x.Env.Target.Type != "host" || x.Env.Target.ID != x.Principal.Subject || !usageCollectionEnabled(ctx, q, x.Principal.Subject) {
		return protocol.Errorf(protocol.CodeForbidden, "negotiated host collection required")
	}
	var p protocol.UsageCollectionObservation
	if err := decodePayload(x.Env, &p); err != nil {
		return err
	}
	run, err := store.GetRun(ctx, q, p.RunID)
	if err != nil {
		return err
	}
	if run.HostID != x.Principal.Subject {
		return protocol.Errorf(protocol.CodeForbidden, "usage run belongs to another host")
	}
	return nil
}
func (ObserveUsageCollection) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.UsageCollectionObservation
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	c := p.Collection
	if p.LocalSourceID == "" || p.ThreadID == "" || p.Sequence < 1 || c.Epoch < 1 || c.Revision < 1 || !slices.Contains([]string{protocol.UsageDormant, protocol.UsageActive, protocol.UsageSettling, protocol.UsageNeedsReconcile, protocol.UsageRetired}, c.State) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid collection observation")
	}
	if c.State == protocol.UsageRetired && (!p.WriterStopped || c.StopProof == "" || !slices.Contains([]string{"complete", "empty", "unavailable"}, c.RetirementReason)) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "retirement requires writer proof and reason")
	}
	var key, session string
	if err := tx.Q().QueryRowContext(ctx, `SELECT source_key,session_id FROM token_usage_sources WHERE host_id=? AND run_id=? AND thread_id=?`, x.Principal.Subject, p.RunID, p.ThreadID).Scan(&key, &session); err != nil {
		return Outcome{}, errNotFound(err, "registered usage source required")
	}
	if session != "" && p.SessionID != "" && session != p.SessionID {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "collection session changed")
	}

	// A control receipt is independent of whether its accompanying observation is
	// superseded; a delayed old request can be closed without overwriting a new epoch.
	if p.ControlRequestID != "" {
		if !slices.Contains([]string{"applied", "stale"}, p.ControlStatus) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid collection command receipt")
		}
		if _, err := tx.Exec(ctx, `UPDATE token_usage_collection_commands SET status=? WHERE request_id=? AND source_key=? AND host_id=? AND status='pending'`, p.ControlStatus, p.ControlRequestID, key, x.Principal.Subject); err != nil {
			return Outcome{}, err
		}
		tx.MarkChanged("token_usage")
	}
	var previous string
	var localID string
	var sequence int64
	err := tx.Q().QueryRowContext(ctx, `SELECT collection_json,local_source_id,observation_sequence FROM token_usage_remote_collection WHERE source_key=?`, key).Scan(&previous, &localID, &sequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	if err == nil {
		if localID != p.LocalSourceID {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "local source identity changed")
		}
		var old protocol.UsageCollection
		if err = json.Unmarshal([]byte(previous), &old); err != nil {
			return Outcome{}, err
		}
		if sequence >= p.Sequence || old.Epoch > c.Epoch || old.Epoch == c.Epoch && old.Revision > c.Revision {
			return Outcome{Result: map[string]bool{"superseded": true}}, nil
		}
	}
	if c.RetirementReason == "empty" {
		var facts bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM token_usage_facts WHERE json_extract(payload_json,'$.thread_id')=?)`, p.ThreadID).Scan(&facts); err != nil {
			return Outcome{}, err
		}
		if facts {
			// An old empty snapshot can arrive after a later execution's upload,
			// even before its collection snapshot. Acknowledge the old request
			// without publishing its superseded empty conclusion.
			return Outcome{Result: map[string]bool{"superseded": true}}, nil
		}
		if c.Evidence != protocol.UsageNoDispatch || len(p.PendingRequestIDs) != 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "empty retirement conflicts with execution evidence")
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return Outcome{}, err
	}
	observation, err := json.Marshal(p)
	if err != nil {
		return Outcome{}, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO token_usage_remote_collection(source_key,host_id,local_source_id,collection_json,observation_json,observation_sequence,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(source_key) DO UPDATE SET collection_json=excluded.collection_json,observation_json=excluded.observation_json,observation_sequence=excluded.observation_sequence,updated_at=excluded.updated_at`, key, x.Principal.Subject, p.LocalSourceID, string(raw), string(observation), p.Sequence, x.Now.UnixMilli())
	if err = usageCollectionChanged(tx, result, err); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]bool{"accepted": true}}, nil
}

func usageCollectionCommands(ctx context.Context, q store.Querier, host string) ([]protocol.UsageCollectionCommand, error) {
	if !usageCollectionEnabled(ctx, q, host) {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT command_json FROM token_usage_collection_commands WHERE host_id=? AND status='pending' ORDER BY created_at,request_id LIMIT 64`, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var commands []protocol.UsageCollectionCommand
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c protocol.UsageCollectionCommand
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		commands = append(commands, c)
	}
	return commands, rows.Err()
}
