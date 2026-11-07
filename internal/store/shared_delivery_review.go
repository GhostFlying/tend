package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

func SharedDeliveryReviewForTarget(ctx context.Context, q Querier, ref protocol.IssueDelivery) (protocol.SharedDeliveryReview, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM shared_delivery_reviews WHERE provider=? AND repository=? AND number=?`, ref.Provider, ref.Repository, ref.Number).Scan(&raw); err != nil {
		return protocol.SharedDeliveryReview{}, notFound(err, "shared delivery review")
	}
	var r protocol.SharedDeliveryReview
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, err
	}
	rows, err := q.QueryContext(ctx, `SELECT issue_id,delivery_id,task_id FROM shared_delivery_review_links WHERE review_id=? ORDER BY issue_id,delivery_id,task_id`, r.ReviewID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	r.Links = []protocol.SharedDeliveryReviewLink{}
	for rows.Next() {
		var link protocol.SharedDeliveryReviewLink
		if err := rows.Scan(&link.IssueID, &link.DeliveryID, &link.TaskID); err != nil {
			return r, err
		}
		r.Links = append(r.Links, link)
	}
	return r, rows.Err()
}

func (t *Tx) PutSharedDeliveryReview(ctx context.Context, r protocol.SharedDeliveryReview, request string) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = t.Exec(ctx, `INSERT INTO shared_delivery_reviews(review_id,provider,repository,number,revision,body_json) VALUES(?,?,?,?,?,?) ON CONFLICT(review_id) DO UPDATE SET revision=excluded.revision,body_json=excluded.body_json`, r.ReviewID, r.Provider, r.Repository, r.Number, r.Revision, string(raw)); err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO shared_delivery_review_history(review_id,revision,request_id,body_json) VALUES(?,?,?,?)`, r.ReviewID, r.Revision, request, string(raw))
	return err
}

// SharedReviewRequirements binds the exact linked business requirements and
// source task agreements; presentation/state changes do not invalidate review.
func SharedReviewRequirements(ctx context.Context, q Querier, r protocol.SharedDeliveryReview) (string, error) {
	seen := map[string]bool{}
	requirements := []string{}
	for _, link := range r.Links {
		i, err := GetCanonicalIssue(ctx, q, link.IssueID)
		if err != nil {
			return "", err
		}
		scope := "issue:" + IssuePRScopeDigest(i.Plan)
		if !seen[scope] {
			seen[scope] = true
			requirements = append(requirements, scope)
		}
		_, currentFacts, err := EffectiveContext(ctx, q, link.IssueID)
		if err != nil {
			return "", err
		}
		currentSemantic, err := semanticSourcedReviewFacts(ctx, q, currentFacts)
		if err != nil {
			return "", err
		}
		for _, fact := range currentSemantic {
			text := "effective:" + fact
			if !seen[text] {
				seen[text] = true
				requirements = append(requirements, text)
			}
		}
		if link.TaskID != "" {
			var raw string
			if err := q.QueryRowContext(ctx, `SELECT s.body_json FROM tasks t JOIN task_specs s ON s.task_id=t.task_id AND s.spec_version=t.spec_version WHERE t.task_id=?`, link.TaskID).Scan(&raw); err != nil {
				return "", fmt.Errorf("shared review task agreement: %w", err)
			}
			var spec struct {
				Scope         string                     `json:"scope"`
				Inputs        []protocol.TaskInput       `json:"inputs,omitempty"`
				SharedContext []protocol.TaskWorkContext `json:"shared_context,omitempty"`
				Goal          string                     `json:"goal"`
				Acceptance    []string                   `json:"acceptance"`
				Constraints   []string                   `json:"constraints"`
				Delivery      string                     `json:"delivery"`
				Deliverables  []string                   `json:"deliverables"`
			}
			if err := json.Unmarshal([]byte(raw), &spec); err != nil {
				return "", err
			}
			for _, context := range spec.SharedContext {
				texts := append([]string{"intent:" + context.Intent}, context.Constraints...)
				facts, err := semanticSourcedReviewFacts(ctx, q, context.Facts)
				if err != nil {
					return "", err
				}
				texts = append(texts, facts...)
				for _, text := range texts {
					key := "shared:" + text
					if !seen[key] {
						seen[key] = true
						requirements = append(requirements, key)
					}
				}
			}
			spec.SharedContext = nil
			for n := range spec.Inputs {
				input := &spec.Inputs[n]
				input.FileID = ""
				input.SourceMessageID = ""
				input.SourceTaskID = ""
				input.SourceArtifactID = ""
				input.SourceAcceptanceID = ""
			}
			encoded, err := json.Marshal(spec)
			if err != nil {
				return "", err
			}
			text := "task:" + string(encoded)
			if !seen[text] {
				seen[text] = true
				requirements = append(requirements, text)
			}
		}
	}
	slices.Sort(requirements)
	raw, err := json.Marshal(requirements)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func SharedDeliveryReviewCurrent(ctx context.Context, q Querier, r *protocol.SharedDeliveryReview) error {
	r.Valid = false
	r.InvalidReason = "formal review has not passed"
	if r.Status != "passed" {
		return nil
	}
	digest, err := SharedReviewRequirements(ctx, q, *r)
	if err != nil {
		return err
	}
	if digest != r.RequirementsDigest && digest != r.AssessedRequirementsDigest {
		r.InvalidReason = "linked delivery requirements changed"
		return nil
	}
	for _, link := range r.Links {
		i, err := GetCanonicalIssue(ctx, q, link.IssueID)
		if err != nil {
			return err
		}
		found := false
		for _, ref := range i.Plan.Deliveries {
			found = found || ref.DeliveryID == link.DeliveryID && ref.Provider == r.Provider && ref.Repository == r.Repository && ref.Number == r.Number
		}
		if !found {
			r.InvalidReason = "delivery link changed"
			return nil
		}
		o, err := GetDeliveryObservation(ctx, q, link.IssueID, link.DeliveryID)
		if err != nil {
			return err
		}
		if o.Observation.ErrorKind != "" || o.Observation.RefreshPending || o.Observation.HeadSHA != r.HeadSHA || o.Observation.BaseSHA != r.BaseSHA {
			r.InvalidReason = "provider candidate changed or is unknown"
			return nil
		}
	}
	r.Valid = true
	r.InvalidReason = ""
	return nil
}

func RequireSharedDeliveryReview(ctx context.Context, q Querier, i protocol.Issue, c protocol.IssueDeliveryCandidate) error {
	var ref protocol.IssueDelivery
	for _, d := range i.Plan.Deliveries {
		if d.DeliveryID == c.DeliveryID {
			ref = d
		}
	}
	r, err := SharedDeliveryReviewForTarget(ctx, q, ref)
	if errors.Is(err, ErrNotFound) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "link the PR's tasks and complete its shared formal review before integration")
	}
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(r.Links, func(l protocol.SharedDeliveryReviewLink) bool {
		return l.IssueID == i.IssueID && l.DeliveryID == c.DeliveryID
	}) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "this Issue is outside the reviewed delivery scope")
	}
	if err := SharedDeliveryReviewCurrent(ctx, q, &r); err != nil {
		return err
	}
	if !r.Valid || r.HeadSHA != c.HeadSHA || r.BaseSHA != c.BaseSHA {
		return protocol.Errorf(protocol.CodeInvalidTransition, "shared PR review is incomplete or no longer matches this candidate")
	}
	return nil
}

// RequireDeliveryDutyReview also proves this source task belongs to the full PR
// review; an Issue-only display association does not silently cover its code.
func RequireDeliveryDutyReview(ctx context.Context, q Querier, i protocol.Issue, d protocol.IssueDeliveryDuty) error {
	if d.Candidate == nil {
		return protocol.Errorf(protocol.CodeInvalidTransition, "delivery candidate is missing")
	}
	if err := RequireSharedDeliveryReview(ctx, q, i, *d.Candidate); err != nil {
		return err
	}
	var ref protocol.IssueDelivery
	for _, entry := range i.Plan.Deliveries {
		if entry.DeliveryID == d.Candidate.DeliveryID {
			ref = entry
		}
	}
	r, err := SharedDeliveryReviewForTarget(ctx, q, ref)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(r.Links, func(link protocol.SharedDeliveryReviewLink) bool {
		return link.IssueID == i.IssueID && link.DeliveryID == d.Candidate.DeliveryID && link.TaskID == d.TaskID
	}) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "link this source task to the complete PR review before integration")
	}
	return nil
}

// RequireDeliveryChecks is a current provider fact, never candidate prose.
// Skipped checks remain visible but cannot establish a successful check.
func RequireDeliveryChecks(ctx context.Context, q Querier, i protocol.Issue, c protocol.IssueDeliveryCandidate) error {
	o, err := GetDeliveryObservation(ctx, q, i.IssueID, c.DeliveryID)
	if err != nil {
		return err
	}
	ci := o.Observation.CI
	if o.Observation.ErrorKind != "" || o.Observation.RefreshPending || ci == nil || ci.HeadSHA != c.HeadSHA || ci.ErrorKind != "" || ci.Truncated || ci.State != "success" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the exact PR head's provider checks have not completed successfully; refresh and retain failed, pending or unknown CI facts")
	}
	passed := false
	for _, check := range ci.Checks {
		state := check.Conclusion
		if state == "" {
			state = check.State
		}
		switch state {
		case "success":
			passed = true
		case "skipped", "neutral":
			// These are known terminal facts, but neither proves success.
		default:
			return protocol.Errorf(protocol.CodeInvalidTransition, "the reviewed PR still has a failed, unfinished or unknown check")
		}
	}
	if !passed {
		return protocol.Errorf(protocol.CodeInvalidTransition, "skipped or missing checks do not establish CI success")
	}
	return nil
}

func semanticSourcedReviewFacts(ctx context.Context, q Querier, facts []protocol.SharedFact) ([]string, error) {
	retained := make([]protocol.SharedFact, 0, len(facts))
	for _, fact := range facts {
		if fact.Kind == protocol.SharedFactDecision {
			applied, err := appliedTransitionReceipt(ctx, q, fact.Source, fact.Text)
			if err != nil {
				return nil, err
			}
			if applied {
				continue
			}
		}
		retained = append(retained, fact)
	}
	return semanticReviewFacts(retained), nil
}
