package reviewwatch

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"strconv"
	"strings"
)

type codebaseActor struct {
	ID       string `json:"Id"`
	Username string `json:"Username"`
}

func (a *codebaseActor) actor() *Actor {
	if a == nil {
		return nil
	}
	return identity(&Actor{ID: a.ID, Login: a.Username})
}

func (c *Client) codebase(ctx context.Context, ref Ref) (Observation, error) {
	body, err := c.cfg.Exec(ctx, c.cfg.CodebaseBin, []string{"--json", "codebase", "mr", "get", strconv.FormatInt(ref.Number, 10), "-R", ref.Repository})
	var response struct {
		Status string `json:"status"`
		Error  *struct {
			Code string `json:"code"`
		} `json:"error"`
		Data struct {
			MR *struct {
				Number                                                    int64
				Status, URL, UpdatedAt, MergedAt, ClosedAt, MergeCommitId string
				MergedBy, ClosedBy                                        *codebaseActor
				SourceBranch, TargetBranch                                *struct {
					Commit *struct {
						ID string `json:"Id"`
					}
				}
				Versions []struct {
					Number         int64
					SourceCommitId string
					TargetCommitId string
				}
			} `json:"merge_request"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil {
		if err != nil {
			return Observation{}, commandError(err)
		}
		return Observation{}, &Error{Kind: "invalid_response"}
	}
	if response.Status != "success" {
		kind := "query_failed"
		if response.Error != nil {
			switch response.Error.Code {
			case "CODEBASE_NOT_FOUND":
				kind = "not_found"
			case "AUTH_REQUIRED", "NOT_AUTHENTICATED", "AUTH_EXPIRED":
				kind = "authentication_required"
			}
		}
		return Observation{}, &Error{Kind: kind}
	}
	if err != nil {
		return Observation{}, commandError(err)
	}
	mr := response.Data.MR
	if mr == nil || mr.Number != ref.Number {
		return Observation{}, &Error{Kind: "invalid_response"}
	}
	if mr.Status != Open && mr.Status != Merged && mr.Status != Closed {
		return Observation{}, &Error{Kind: "unknown_state"}
	}
	out := Observation{State: mr.Status, URL: mr.URL, UpdatedAt: mr.UpdatedAt, MergedAt: mr.MergedAt, ClosedAt: mr.ClosedAt, MergeSHA: mr.MergeCommitId, MergedBy: mr.MergedBy.actor()}
	if mr.TargetBranch != nil && mr.TargetBranch.Commit != nil {
		if !c.cfg.IncludeChecks {
			out.BaseSHA = mr.TargetBranch.Commit.ID
		}
		out.TargetSHA = mr.TargetBranch.Commit.ID
		out.TargetContainsMerge = mr.Status == Merged && out.MergeSHA != "" && out.TargetSHA == out.MergeSHA
	}
	// The source branch can advance after the review has ended. Its live head
	// must not replace the commit recorded in the latest review version.
	var latest int64
	for _, v := range mr.Versions {
		if v.Number > latest && v.SourceCommitId != "" {
			latest = v.Number
			out.HeadSHA = v.SourceCommitId
			if c.cfg.IncludeChecks {
				out.BaseSHA = v.TargetCommitId
			}
		}
	}
	if out.HeadSHA == "" && mr.SourceBranch != nil && mr.SourceBranch.Commit != nil {
		out.HeadSHA = mr.SourceBranch.Commit.ID
	}
	if mr.Status == Closed {
		out.ClosedBy = mr.ClosedBy.actor()
	}
	if c.cfg.IncludeChecks && out.State == Merged && out.MergeSHA != "" && out.TargetSHA != "" && !out.TargetContainsMerge {
		confirmed, err := c.codebaseLineage(ctx, ref, out.TargetSHA, out.MergeSHA)
		if err != nil {
			out.LineageErrorKind = "query_failed"
			var failure *Error
			if errors.As(err, &failure) {
				out.LineageErrorKind = failure.Kind
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				out.LineageErrorKind = "timeout"
			}
		} else {
			out.TargetContainsMerge = confirmed
		}
	}
	if c.cfg.IncludeChecks {
		if err := c.codebaseChecks(ctx, ref, &out); err != nil {
			kind := "query_failed"
			var failure *Error
			if errors.As(err, &failure) {
				kind = failure.Kind
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				kind = "timeout"
			}
			out.CI = &protocol.DeliveryCI{HeadSHA: out.HeadSHA, State: "unknown", Checks: []protocol.DeliveryCheck{}, ErrorKind: kind}
		}
	}
	return out, nil
}

// The status command provides a bounded first page tied to source_commit_id.
// Its all_passed summary is not sufficient: unknown conclusions remain unknown.
func (c *Client) codebaseChecks(ctx context.Context, ref Ref, out *Observation) error {
	body, err := c.cfg.Exec(ctx, c.cfg.CodebaseBin, []string{"--json", "codebase", "mr", "status", strconv.FormatInt(ref.Number, 10), "-R", ref.Repository, "--page-size", "100"})
	var response struct {
		Status string
		Error  *struct{ Code string }
		Data   struct {
			MergeRequest struct {
				Number         int64
				Status         string
				SourceCommitID string `json:"source_commit_id"`
			} `json:"merge_request"`
			Mergeability struct{ Mergeable *bool } `json:"mergeability"`
			CheckRuns    struct {
				TotalCount int `json:"total_count"`
				Items      []struct{ Name, Status, Conclusion, DetailsURL string }
			} `json:"check_runs"`
		}
	}
	if json.Unmarshal(body, &response) != nil {
		if err != nil {
			return commandError(err)
		}
		return &Error{Kind: "invalid_response"}
	}
	if response.Status != "success" {
		if response.Error != nil && strings.HasPrefix(response.Error.Code, "AUTH_") {
			return &Error{Kind: "authentication_required"}
		}
		if response.Error != nil && strings.Contains(response.Error.Code, "RATE_LIMIT") {
			return &Error{Kind: "rate_limited"}
		}
		return &Error{Kind: "query_failed"}
	}
	if err != nil {
		return commandError(err)
	}
	if response.Data.MergeRequest.Number != ref.Number || response.Data.MergeRequest.SourceCommitID == "" || response.Data.MergeRequest.SourceCommitID != out.HeadSHA || response.Data.MergeRequest.Status != out.State {
		return &Error{Kind: "query_failed"}
	}
	if response.Data.Mergeability.Mergeable != nil {
		out.Mergeability = "conflicting"
		if *response.Data.Mergeability.Mergeable {
			out.Mergeability = "mergeable"
		}
	}
	checks := response.Data.CheckRuns
	out.CI = &protocol.DeliveryCI{HeadSHA: out.HeadSHA, State: "unknown", Checks: []protocol.DeliveryCheck{}, Truncated: checks.TotalCount > len(checks.Items)}
	unknown, pending, failed, passed := false, false, false, false
	for _, r := range checks.Items {
		state, conclusion := strings.ToLower(r.Status), strings.ToLower(r.Conclusion)
		out.CI.Checks = append(out.CI.Checks, protocol.DeliveryCheck{Name: r.Name, State: state, Conclusion: conclusion, URL: r.DetailsURL})
		if state == "queued" || state == "in_progress" {
			pending = true
			continue
		}
		if state != "completed" {
			unknown = true
			continue
		}
		switch conclusion {
		case "success", "neutral":
			passed = true
		case "skipped":
		case "failure", "cancelled", "timed_out", "action_required", "startup_failure":
			failed = true
		default:
			unknown = true
		}
	}
	if !out.CI.Truncated && len(checks.Items) > 0 {
		switch {
		case failed:
			out.CI.State = "failure"
		case pending:
			out.CI.State = "pending"
		case unknown:
		case passed:
			out.CI.State = "success"
		default:
			out.CI.State = "skipped"
		}
	}
	return nil
}

// No commit reachable from merge but not from target proves that target
// contains merge. An explicit first-page array and immutable range are required.
func (c *Client) codebaseLineage(ctx context.Context, ref Ref, target, merge string) (bool, error) {
	body, err := c.cfg.Exec(ctx, c.cfg.CodebaseBin, []string{"--json", "codebase", "commit", "list", "-R", ref.Repository, "--base", target, "--target", merge, "--page", "1", "--page-size", "1"})
	if err != nil {
		return false, commandError(err)
	}
	var response struct {
		Status string
		Data   struct {
			RefName    string             `json:"ref_name"`
			PageNumber int                `json:"page_number"`
			Commits    *[]json.RawMessage `json:"commits"`
		}
	}
	if json.Unmarshal(body, &response) != nil || response.Status != "success" || response.Data.RefName != target+".."+merge || response.Data.PageNumber != 1 || response.Data.Commits == nil {
		return false, &Error{Kind: "invalid_response"}
	}
	return len(*response.Data.Commits) == 0, nil
}
