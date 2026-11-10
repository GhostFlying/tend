package store

import (
	"context"
	"errors"
)

// GetOwnerRequest resolves the original namespace without copying a main-Agent
// request into the owner namespace. Run- and machine-scoped requests are excluded.
func GetOwnerRequest(ctx context.Context, q Querier, id string) (RequestRecord, error) {
	record, err := GetRequest(ctx, q, "owner", id)
	if !errors.Is(err, ErrNotFound) {
		return record, err
	}
	var domain string
	err = q.QueryRowContext(ctx, `SELECT domain FROM request_records WHERE request_id=? AND json_extract(principal_json,'$.kind')='main_agent' AND domain LIKE 'main_agent:%'`, id).Scan(&domain)
	if err != nil {
		return RequestRecord{}, notFound(err, "owner request")
	}
	return GetRequest(ctx, q, domain, id)
}
