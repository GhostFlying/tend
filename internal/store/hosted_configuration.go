package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

// HostedConfiguration is server-local. Public is the only owner-facing value.
type HostedConfiguration struct {
	Revision    int64
	PrivateHash string
	PrivateJSON []byte
	Public      protocol.AgentInventoryProjection
	MainAgentID string
	RequestID   string
	AcceptedAt  int64
}

func GetHostedConfiguration(ctx context.Context, q Querier, revision int64) (HostedConfiguration, error) {
	var row HostedConfiguration
	var private, public string
	query := `SELECT revision,private_hash,private_json,public_json,main_agent_id,request_id,accepted_at FROM hosted_configuration_versions WHERE revision=?`
	args := []any{revision}
	if revision == 0 {
		query = `SELECT revision,private_hash,private_json,public_json,main_agent_id,request_id,accepted_at FROM hosted_configuration_versions ORDER BY revision DESC LIMIT 1`
		args = nil
	}
	err := q.QueryRowContext(ctx, query, args...).Scan(&row.Revision, &row.PrivateHash, &private, &public, &row.MainAgentID, &row.RequestID, &row.AcceptedAt)
	if err != nil {
		return row, notFound(err, "hosted configuration")
	}
	row.PrivateJSON = []byte(private)
	err = json.Unmarshal([]byte(public), &row.Public)
	return row, err
}
func (t *Tx) InsertHostedConfiguration(ctx context.Context, p *hostedconfig.Prepared, request string, now int64) error {
	public, err := json.Marshal(p.Public())
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO hosted_configuration_versions(revision,environment_id,private_hash,private_json,public_json,main_agent_id,request_id,accepted_at) VALUES(?,?,?,?,?,?,?,?)`, p.Revision(), p.Public().ExecutionEnvironmentID, p.Hash(), string(p.PrivateJSON()), string(public), p.MainAgentID(), request, now)
	return err
}

type HostedExecution struct {
	ID              string
	ConversationID  string
	IssueID         string
	AssignmentEpoch int64
	Selection       hostedconfig.Selection
	CreatedAt       int64
}

func GetHostedExecution(ctx context.Context, q Querier, id string) (HostedExecution, error) {
	var row HostedExecution
	var raw string
	err := q.QueryRowContext(ctx, `SELECT execution_id,conversation_id,COALESCE(issue_id,''),COALESCE(assignment_epoch,0),private_snapshot,created_at FROM hosted_executions WHERE execution_id=?`, id).Scan(&row.ID, &row.ConversationID, &row.IssueID, &row.AssignmentEpoch, &raw, &row.CreatedAt)
	if err != nil {
		return row, notFound(err, "hosted execution")
	}
	err = json.Unmarshal([]byte(raw), &row.Selection)
	return row, err
}
func (t *Tx) InsertHostedExecution(ctx context.Context, row HostedExecution) error {
	raw, err := json.Marshal(row.Selection)
	if err != nil {
		return err
	}
	var issue, epoch any
	if row.IssueID != "" {
		issue, epoch = row.IssueID, row.AssignmentEpoch
	}
	table := "local_hosted_executions"
	if row.Selection.ConfigurationRevision == 0 {
		table = "runner_coordination_executions"
	}
	_, err = t.Exec(ctx, `INSERT INTO `+table+`(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, row.ID, row.ConversationID, issue, epoch, string(row.Selection.Purpose), row.Selection.ConfigurationRevision, hostedconfig.IdentityRef(row.Selection.Identity), string(raw), row.CreatedAt)
	return err
}

// HostedIssueScope is execution ownership, never inferred from shared source links.
func HostedIssueScope(ctx context.Context, q Querier, request string) (string, error) {
	var issue string
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT issue_id FROM hosted_executions WHERE execution_id=? AND role='issue_manager'),'')`, request).Scan(&issue)
	return issue, err
}

// HostedCoordinationIdentity survives a native repair or rate-limit continuation.
// Source messages never define this effect namespace.
func HostedCoordinationIdentity(ctx context.Context, q Querier, request string) (string, error) {
	roots, err := FollowupIDs(ctx, q, `SELECT original.request_id FROM logical_turn_executions current JOIN logical_turn_executions original ON original.logical_turn_id=current.logical_turn_id WHERE current.request_id=? AND original.repair=0 ORDER BY original.rowid LIMIT 1`, request)
	if err != nil {
		return "", err
	}
	root := request
	if len(roots) > 0 {
		root = roots[0]
	}
	recovery, err := RecoveryForRequest(ctx, q, root)
	if err == nil {
		return recovery.RootRequestID, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	return root, nil
}
