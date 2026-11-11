package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// creationID is the identity of the actual object, supplied before admission.
// Request IDs remain tracing provenance and never allocate business objects.
func creationID(x *Exec, prefix protocol.Prefix) (string, error) {
	id := x.Env.Target.ID
	if !protocol.ValidID(prefix, id) {
		return "", protocol.Errorf(protocol.CodeInvalidRequest, "target.id must identify the new %s object; retain it across retries", prefix)
	}
	return id, nil
}

func sameBusinessDefinition(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil && reflect.DeepEqual(l, r)
}

func existingIssueCreation(ctx context.Context, q store.Querier, id string, definition protocol.IssuePlan) (*protocol.WorkItem, error) {
	current, err := store.GetIssue(ctx, q, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw string
	if err = q.QueryRowContext(ctx, `SELECT body_json FROM work_item_revisions WHERE work_item_id=? AND revision=1`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var original protocol.WorkItem
	if err = json.Unmarshal([]byte(raw), &original); err != nil {
		return nil, err
	}
	if !sameBusinessDefinition(original.IssuePlan, definition) {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "Issue identity already has another original definition")
	}
	return &current, nil
}
