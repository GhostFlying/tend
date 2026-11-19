package views

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type boardPullRequestFact struct {
	state       string
	providerErr string
	draft       sql.NullBool
	formal      bool
}

func readBoardPullRequests(ctx context.Context, q store.Querier, ids []string) (map[string]map[string]boardPullRequestFact, error) {
	page, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
 SELECT r.issue_id, r.delivery_id, COALESCE(json_extract(j.observation_json,'$.state'),''),
        COALESCE(json_extract(j.observation_json,'$.error_kind'),''),
        json_extract(j.observation_json,'$.draft'),
        c.state IN ('active','paused') AND (
          EXISTS (SELECT 1 FROM issue_delivery_duties d WHERE d.issue_id=r.issue_id
            AND d.status IN ('preparing','awaiting_owner','authorized','executing')
            AND json_extract(d.body_json,'$.assignment_epoch')=c.assignment_epoch
            AND json_extract(d.body_json,'$.candidate.delivery_id')=r.delivery_id)
          OR EXISTS (SELECT 1 FROM shared_delivery_review_links l
            JOIN shared_delivery_reviews s ON s.review_id=l.review_id
            WHERE l.issue_id=r.issue_id AND l.delivery_id=r.delivery_id
            AND json_extract(s.body_json,'$.status') IN ('reviewing','changes_requested','waiting'))
        )
 FROM json_each(?) page
 JOIN server_delivery_references r ON r.issue_id=page.value AND r.provider='github'
 JOIN issue_controls c ON c.issue_id=r.issue_id
 LEFT JOIN server_delivery_jobs j USING(provider,repository,number)`, string(page))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]map[string]boardPullRequestFact)
	for rows.Next() {
		var issueID, deliveryID string
		var fact boardPullRequestFact
		if err := rows.Scan(&issueID, &deliveryID, &fact.state, &fact.providerErr, &fact.draft, &fact.formal); err != nil {
			return nil, err
		}
		if out[issueID] == nil {
			out[issueID] = make(map[string]boardPullRequestFact)
		}
		out[issueID][deliveryID] = fact
	}
	return out, rows.Err()
}

func boardPullRequests(deliveries []protocol.IssueDelivery, facts map[string]boardPullRequestFact) []protocol.IssueBoardPullRequest {
	type ranked struct {
		pr   protocol.IssueBoardPullRequest
		rank int
	}
	var prs []ranked
	seen := make(map[string]int)
	for _, ref := range deliveries {
		if ref.Provider != "github" {
			continue
		}
		link := protocol.GitHubPullRequestURL(ref.Repository, ref.Number)
		if link == "" {
			continue
		}
		// Historical plans may contain several reference IDs for the same PR.
		key := strings.ToLower(link)
		fact := facts[ref.DeliveryID]
		status := protocol.IssueBoardPRUnknown
		if fact.providerErr == "" {
			switch fact.state {
			case "merged", "closed":
				status = protocol.IssueBoardPRStatus(fact.state)
			case "open":
				if fact.draft.Valid {
					status = protocol.IssueBoardPROpen
					if fact.draft.Bool {
						status = protocol.IssueBoardPRDraft
					}
				}
			}
		}
		rank := 2
		if fact.state == "open" {
			rank = 1
		}
		if fact.formal {
			rank = 0
		}
		if i, duplicate := seen[key]; duplicate {
			// Let an active legacy alias promote the same PR without changing
			// its original list position at equal priority.
			prs[i].rank = min(prs[i].rank, rank)
			if prs[i].pr.Status == protocol.IssueBoardPRUnknown {
				prs[i].pr.Status = status
			}
			continue
		}
		seen[key] = len(prs)
		prs = append(prs, ranked{pr: protocol.IssueBoardPullRequest{Repository: ref.Repository, Number: ref.Number, URL: link, Status: status}, rank: rank})
	}
	slices.SortStableFunc(prs, func(a, b ranked) int { return a.rank - b.rank })
	out := make([]protocol.IssueBoardPullRequest, len(prs))
	for i, item := range prs {
		out[i] = item.pr
	}
	return out
}
