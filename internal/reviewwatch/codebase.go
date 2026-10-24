package reviewwatch

import (
	"context"
	"encoding/json"
	"strconv"
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
				SourceBranch                                              *struct {
					Commit *struct {
						ID string `json:"Id"`
					}
				}
				Versions []struct {
					Number         int64
					SourceCommitId string
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
	// The source branch can advance after the review has ended. Its live head
	// must not replace the commit recorded in the latest review version.
	var latest int64
	for _, v := range mr.Versions {
		if v.Number > latest && v.SourceCommitId != "" {
			latest = v.Number
			out.HeadSHA = v.SourceCommitId
		}
	}
	if out.HeadSHA == "" && mr.SourceBranch != nil && mr.SourceBranch.Commit != nil {
		out.HeadSHA = mr.SourceBranch.Commit.ID
	}
	if mr.Status == Closed {
		out.ClosedBy = mr.ClosedBy.actor()
	}
	return out, nil
}
