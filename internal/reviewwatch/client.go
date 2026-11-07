// Package reviewwatch observes external code reviews without starting a worker.
package reviewwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
)

const (
	GitHub   = "github"
	Codebase = "codebase"
	Open     = "open"
	Merged   = "merged"
	Closed   = "closed"
)

// Ref is an explicit remote identity; no checkout or current branch is used.
type Ref struct {
	Provider   string
	Repository string
	Number     int64
}

// Actor contains only identity, never email addresses or other account data.
type Actor struct {
	ID    string `json:"id,omitempty"`
	Login string `json:"login,omitempty"`
}

// Observation records provider facts. Missing actors/times remain unknown.
type Observation struct {
	LineageErrorKind    string               `json:"lineage_error_kind,omitempty"`
	Mergeability        string               `json:"mergeability,omitempty"`
	CI                  *protocol.DeliveryCI `json:"ci,omitempty"`
	TargetSHA           string               `json:"target_sha,omitempty"`
	TargetContainsMerge bool                 `json:"target_contains_merge,omitempty"`
	BaseSHA             string               `json:"base_sha,omitempty"`
	State               string               `json:"state"`
	HeadSHA             string               `json:"head_sha,omitempty"`
	MergeSHA            string               `json:"merge_sha,omitempty"`
	URL                 string               `json:"url,omitempty"`
	UpdatedAt           string               `json:"updated_at,omitempty"`
	MergedAt            string               `json:"merged_at,omitempty"`
	ClosedAt            string               `json:"closed_at,omitempty"`
	MergedBy            *Actor               `json:"merged_by,omitempty"`
	ClosedBy            *Actor               `json:"closed_by,omitempty"`
}

// Exec returns stdout even for a nonzero CLI exit so structured errors can be
// classified. Implementations must honor ctx and must not log credentials.
type Exec func(context.Context, string, []string) ([]byte, error)

type Config struct {
	IncludeChecks bool
	GitHubBin     string
	CodebaseBin   string
	Timeout       time.Duration
	Exec          Exec
}

type Client struct{ cfg Config }

func New(cfg Config) *Client {
	if cfg.GitHubBin == "" {
		cfg.GitHubBin = "gh"
	}
	if cfg.CodebaseBin == "" {
		cfg.CodebaseBin = "bytedcli"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Exec == nil {
		cfg.Exec = func(ctx context.Context, bin string, args []string) ([]byte, error) {
			if bin != cfg.CodebaseBin {
				return execute(ctx, bin, args)
			}
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env = append(os.Environ(), "BYTEDCLI_NO_AUTO_UPGRADE=1")
			procx.Contain(cmd)
			defer procx.KillGroup(cmd)
			return cmd.Output()
		}
	}
	return &Client{cfg: cfg}
}

// Error exposes a safe category only. Provider output is never an error string.
type Error struct{ Kind string }

func (e *Error) Error() string { return "review observation: " + e.Kind }

func execute(ctx context.Context, bin string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	procx.Contain(cmd)
	defer procx.KillGroup(cmd)
	return cmd.Output()
}

func (c *Client) Observe(ctx context.Context, ref Ref) (Observation, error) {
	if ref.Number <= 0 || ref.Repository == "" || strings.HasPrefix(ref.Repository, "-") || strings.ContainsAny(ref.Repository, "\x00\r\n") {
		return Observation{}, &Error{Kind: "invalid_reference"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	var result Observation
	var err error
	switch ref.Provider {
	case GitHub:
		result, err = c.github(ctx, ref)
	case Codebase:
		result, err = c.codebase(ctx, ref)
	default:
		return Observation{}, &Error{Kind: "unsupported_provider"}
	}
	if ctx.Err() != nil && !(result.State != "" && ((result.CI != nil && result.CI.ErrorKind != "") || result.LineageErrorKind != "")) {
		return Observation{}, fmt.Errorf("review observation cancelled: %w", ctx.Err())
	}
	return result, err
}

func commandError(err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return &Error{Kind: "cli_unavailable"}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		stderr := strings.ToLower(string(exit.Stderr))
		if strings.Contains(stderr, "429") || strings.Contains(stderr, "rate limit") {
			return &Error{Kind: "rate_limited"}
		}
		if strings.Contains(stderr, "401") || strings.Contains(stderr, "authenticate") || strings.Contains(stderr, "not logged") || strings.Contains(stderr, "gh auth login") {
			return &Error{Kind: "authentication_required"}
		}
		if strings.Contains(stderr, "resolve host") || strings.Contains(stderr, "connection refused") || strings.Contains(stderr, "network is unreachable") {
			return &Error{Kind: "unreachable"}
		}
	}
	return &Error{Kind: "query_failed"}
}

const githubChecksQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){number url state headRefOid baseRefOid baseRef{target{oid}} mergeable mergeStateStatus commits(last:1){nodes{commit{oid statusCheckRollup{state contexts(first:100){totalCount nodes{... on CheckRun{name status conclusion detailsUrl} ... on StatusContext{context state targetUrl}}}}}}} updatedAt closedAt mergedAt mergeCommit{oid} mergedBy{login ... on User{id} ... on Bot{id}} timelineItems(last:1,itemTypes:[CLOSED_EVENT]){nodes{... on ClosedEvent{createdAt actor{login ... on User{id} ... on Bot{id}}}}}}}}`

const githubQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){number url state headRefOid baseRefOid baseRef{target{oid}} updatedAt closedAt mergedAt mergeCommit{oid} mergedBy{login ... on User{id} ... on Bot{id}} timelineItems(last:1,itemTypes:[CLOSED_EVENT]){nodes{... on ClosedEvent{createdAt actor{login ... on User{id} ... on Bot{id}}}}}}}}`

func (c *Client) github(ctx context.Context, ref Ref) (Observation, error) {
	parts := strings.Split(ref.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Observation{}, &Error{Kind: "invalid_reference"}
	}
	query := githubQuery
	if c.cfg.IncludeChecks {
		query = githubChecksQuery
	}
	body, err := c.cfg.Exec(ctx, c.cfg.GitHubBin, []string{"api", "graphql", "-f", "query=" + query, "-f", "owner=" + parts[0], "-f", "repo=" + parts[1], "-F", "number=" + strconv.FormatInt(ref.Number, 10)})
	var response struct {
		Data struct {
			Repository *struct {
				PullRequest *struct {
					Number                      int64 `json:"number"`
					Mergeable, MergeStateStatus string
					Commits                     struct {
						Nodes []struct {
							Commit struct {
								OID               string
								StatusCheckRollup *struct {
									State    string
									Contexts struct {
										TotalCount int
										Nodes      []struct{ Name, Status, Conclusion, DetailsURL, Context, State, TargetURL string }
									}
								}
							}
						}
					}
					URL, State, HeadRefOid, BaseRefOid, UpdatedAt, ClosedAt, MergedAt string
					BaseRef                                                           *struct{ Target *struct{ OID string } }
					MergeCommit                                                       *struct {
						OID string `json:"oid"`
					}
					MergedBy      *Actor
					TimelineItems struct {
						Nodes []struct {
							CreatedAt string
							Actor     *Actor
						}
					}
				}
			}
		}
		Errors []struct{ Type string }
	}
	if json.Unmarshal(body, &response) != nil {
		if err != nil {
			return Observation{}, commandError(err)
		}
		return Observation{}, &Error{Kind: "invalid_response"}
	}
	if len(response.Errors) > 0 {
		kind := "query_failed"
		for _, e := range response.Errors {
			if e.Type == "RATE_LIMITED" {
				kind = "rate_limited"
			}
			if e.Type == "NOT_FOUND" {
				kind = "not_found"
			}
			if e.Type == "FORBIDDEN" || e.Type == "UNAUTHORIZED" {
				kind = "authentication_required"
			}
		}
		return Observation{}, &Error{Kind: kind}
	}
	if err != nil {
		return Observation{}, commandError(err)
	}
	if response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
		return Observation{}, &Error{Kind: "not_found"}
	}
	pr := response.Data.Repository.PullRequest
	if pr.Number != ref.Number {
		return Observation{}, &Error{Kind: "invalid_response"}
	}
	state := strings.ToLower(pr.State)
	if state != Open && state != Merged && state != Closed {
		return Observation{}, &Error{Kind: "unknown_state"}
	}
	out := Observation{Mergeability: strings.ToLower(pr.Mergeable), BaseSHA: pr.BaseRefOid, State: state, HeadSHA: pr.HeadRefOid, URL: pr.URL, UpdatedAt: pr.UpdatedAt, MergedAt: pr.MergedAt, ClosedAt: pr.ClosedAt, MergedBy: identity(pr.MergedBy)}
	if len(pr.Commits.Nodes) == 1 {
		commit := pr.Commits.Nodes[0].Commit
		if commit.OID == out.HeadSHA && commit.StatusCheckRollup != nil {
			rollup := commit.StatusCheckRollup
			out.CI = &protocol.DeliveryCI{HeadSHA: commit.OID, State: strings.ToLower(rollup.State), Checks: []protocol.DeliveryCheck{}, Truncated: rollup.Contexts.TotalCount > len(rollup.Contexts.Nodes)}
			for _, check := range rollup.Contexts.Nodes {
				if check.Name != "" {
					out.CI.Checks = append(out.CI.Checks, protocol.DeliveryCheck{Name: check.Name, State: strings.ToLower(check.Status), Conclusion: strings.ToLower(check.Conclusion), URL: check.DetailsURL})
				} else {
					out.CI.Checks = append(out.CI.Checks, protocol.DeliveryCheck{Name: check.Context, State: strings.ToLower(check.State), URL: check.TargetURL})
				}
			}
			if out.CI.Truncated {
				out.CI.State = "unknown"
			}
		}
	}
	if pr.BaseRef != nil && pr.BaseRef.Target != nil {
		out.TargetSHA = pr.BaseRef.Target.OID
	}
	if pr.MergeCommit != nil {
		out.MergeSHA = pr.MergeCommit.OID
	}
	if state == Merged && out.MergeSHA != "" && out.TargetSHA != "" {
		// Compare immutable commits: a later branch move cannot change the
		// lineage proof for the target observed in the GraphQL snapshot.
		body, err := c.cfg.Exec(ctx, c.cfg.GitHubBin, []string{"api", "repos/" + ref.Repository + "/compare/" + out.MergeSHA + "..." + out.TargetSHA})
		if err != nil {
			if c.cfg.IncludeChecks {
				out.LineageErrorKind = "query_failed"
				var failure *Error
				if errors.As(commandError(err), &failure) {
					out.LineageErrorKind = failure.Kind
				}
				if ctx.Err() != nil {
					out.LineageErrorKind = "timeout"
				}
				return out, nil
			}
			return Observation{}, commandError(err)
		}
		var comparison struct {
			Status          string
			MergeBaseCommit struct {
				SHA string `json:"sha"`
			} `json:"merge_base_commit"`
		}
		if json.Unmarshal(body, &comparison) != nil {
			if c.cfg.IncludeChecks {
				out.LineageErrorKind = "invalid_response"
				return out, nil
			}
			return Observation{}, &Error{Kind: "invalid_response"}
		}
		out.TargetContainsMerge = (comparison.Status == "ahead" || comparison.Status == "identical") && comparison.MergeBaseCommit.SHA == out.MergeSHA
	}
	// A merged review is distinct from an explicit close. Never attribute it to
	// an earlier close/reopen cycle or substitute its merger as the closing user.
	if state == Closed && len(pr.TimelineItems.Nodes) > 0 {
		event := pr.TimelineItems.Nodes[len(pr.TimelineItems.Nodes)-1]
		if event.CreatedAt == pr.ClosedAt {
			out.ClosedBy = identity(event.Actor)
		}
	}
	return out, nil
}

func identity(a *Actor) *Actor {
	if a == nil || a.ID == "" && a.Login == "" {
		return nil
	}
	return a
}
