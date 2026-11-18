package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

// IssueShortCode reads the permanent identity shared by all Issue revisions.
func IssueShortCode(ctx context.Context, q Querier, id string) (string, error) {
	var number int64
	if err := q.QueryRowContext(ctx, `SELECT number FROM issue_short_numbers WHERE issue_id=?`, id).Scan(&number); err != nil {
		return "", notFound(err, "Issue short number "+id)
	}
	return protocol.IssueShortCode(number), nil
}

// IssueIdentity reads the fields used by human links without loading live execution.
func IssueIdentity(ctx context.Context, q Querier, id string) (protocol.IssueIdentity, error) {
	var out protocol.IssueIdentity
	var number int64
	err := q.QueryRowContext(ctx, `SELECT w.work_item_id,n.number,COALESCE(json_extract(w.body_json,'$.title'),'')
		FROM work_items w JOIN issue_short_numbers n ON n.issue_id=w.work_item_id WHERE w.work_item_id=?`, id).Scan(&out.IssueID, &number, &out.Title)
	if err != nil {
		return out, notFound(err, "Issue "+id)
	}
	out.ShortCode = protocol.IssueShortCode(number)
	return out, nil
}

// ResolveIssueLookup is for human-facing reads. Commands and stored references
// continue to require the original internal ID.
func ResolveIssueLookup(ctx context.Context, q Querier, input string) (string, error) {
	digits, _ := protocol.IssueNumberInput(input, true)
	if digits == "" {
		return input, nil
	}
	number, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return "", protocol.Errorf(protocol.CodeNotFound, "Issue short number is out of range")
	}
	var id string
	if err := q.QueryRowContext(ctx, `SELECT issue_id FROM issue_short_numbers WHERE number=?`, number).Scan(&id); err != nil {
		return "", notFound(err, fmt.Sprintf("Issue %s", protocol.IssueShortCode(number)))
	}
	return id, nil
}
