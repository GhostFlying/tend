package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// OwnerResultPolicy preserves this exact result's quality agreement even when
// the continuing owner's next result chooses a different review arrangement.
func OwnerResultPolicy(ctx context.Context, q Querier, artifact string) (*protocol.IssueAcceptancePolicy, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT policy_json FROM issue_owner_results WHERE artifact_id=?`, artifact).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var policy protocol.IssueAcceptancePolicy
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// OwnerResultEvidence reads a real owner's registered result, without ending
// its continuing coordination Task or manufacturing an independent verdict.
func OwnerResultEvidence(ctx context.Context, q Querier, artifact string) (bool, error) {
	var valid bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_owner_results o JOIN artifacts a USING(artifact_id) JOIN runs r ON r.run_id=a.run_id JOIN issue_manager_tasks m ON m.task_id=a.task_id WHERE o.artifact_id=? AND r.role='issue_manager' AND m.issue_id=o.issue_id AND (o.required_independent=0 OR o.acceptance_id IS NOT NULL OR o.reviewer_artifact_id IS NOT NULL))`, artifact).Scan(&valid)
	return valid, err
}

// OwnerResultReviewReferences names the actual independent original rather
// than presenting a report as a synthetic acceptance of the manager Task.
func OwnerResultReviewReferences(ctx context.Context, q Querier, artifact string) (reviewArtifact, acceptance string, err error) {
	err = q.QueryRowContext(ctx, `SELECT COALESCE(reviewer_artifact_id,''),COALESCE(acceptance_id,'') FROM issue_owner_results WHERE artifact_id=?`, artifact).Scan(&reviewArtifact, &acceptance)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func ownerTaskResultEvidence(ctx context.Context, q Querier, task TaskRow, summary string) (protocol.IssueEvidence, error) {
	valid, err := OwnerResultEvidence(ctx, q, task.CurrentArtifactID)
	if err != nil {
		return protocol.IssueEvidence{}, err
	}
	if !valid {
		return protocol.IssueEvidence{}, protocol.Errorf(protocol.CodeInvalidTransition, "the owner result needs its actual required independent acceptance")
	}
	return protocol.IssueEvidence{Kind: protocol.WorkItemEvidenceResult, Reference: task.CurrentArtifactID, Summary: summary}, nil
}
