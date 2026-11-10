package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// GetOwnerRequest resolves the original namespace without copying a main-Agent
// request into the owner namespace. Run- and machine-scoped requests are excluded.
func GetOwnerRequest(ctx context.Context, q Querier, id string) (RequestRecord, error) {
	record, err := GetRequest(ctx, q, "owner", id)
	if !errors.Is(err, ErrNotFound) {
		return record, err
	}
	var domain string
	err = q.QueryRowContext(ctx, `SELECT domain FROM request_records WHERE request_id=? AND domain GLOB 'main_agent:*' AND json_extract(principal_json,'$.kind')='main_agent'`, id).Scan(&domain)
	if err != nil {
		return RequestRecord{}, notFound(err, "owner request")
	}
	return GetRequest(ctx, q, domain, id)
}

// HasOwnerRequestAuthority recognizes a successful real owner request or a
// server-bound main-owner call without promoting ordinary Agent requests.
func HasOwnerRequestAuthority(ctx context.Context, q Querier, id string) (bool, error) {
	record, err := GetOwnerRequest(ctx, q, id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if record.Status != protocol.OpSucceeded && record.Status != protocol.OpAccepted {
		return false, nil
	}
	var principal protocol.Principal
	if err := json.Unmarshal([]byte(record.PrincipalJSON), &principal); err != nil {
		return false, err
	}
	if principal.Kind == protocol.PrincipalOwner {
		return true, nil
	}
	if principal.Kind != protocol.PrincipalMainAgent {
		return false, nil
	}
	var delegated bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_owner_effects WHERE request_id=?)`, id).Scan(&delegated)
	return delegated, err
}
