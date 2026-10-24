// Package reviewwatch observes external code reviews without starting a worker.
package reviewwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
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
	State     string `json:"state"`
	HeadSHA   string `json:"head_sha,omitempty"`
	MergeSHA  string `json:"merge_sha,omitempty"`
	URL       string `json:"url,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	MergedAt  string `json:"merged_at,omitempty"`
	ClosedAt  string `json:"closed_at,omitempty"`
	MergedBy  *Actor `json:"merged_by,omitempty"`
	ClosedBy  *Actor `json:"closed_by,omitempty"`
}

// Exec returns stdout even for a nonzero CLI exit so structured errors can be
// classified. Implementations must honor ctx and must not log credentials.
type Exec func(context.Context, string, []string) ([]byte, error)

type Config struct {
	GitHubBin   string
	CodebaseBin string
	Timeout     time.Duration
	Exec        Exec
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
		cfg.Exec = execute
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
	if ctx.Err() != nil {
		return Observation{}, fmt.Errorf("review observation cancelled: %w", ctx.Err())
	}
	return result, err
}

func commandError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &Error{Kind: "cli_unavailable"}
	}
	return &Error{Kind: "query_failed"}
}

const githubQuery = `query($owner:String!,$repo:String!,$number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$number){number url state headRefOid updatedAt closedAt mergedAt mergeCommit{oid} mergedBy{login ... on User{id} ... on Bot{id}} timelineItems(last:1,itemTypes:[CLOSED_EVENT]){nodes{... on ClosedEvent{createdAt actor{login ... on User{id} ... on Bot{id}}}}}}}}`

func (c *Client) github(ctx context.Context, ref Ref) (Observation, error) {
	parts := strings.Split(ref.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Observation{}, &Error{Kind: "invalid_reference"}
	}
	body, err := c.cfg.Exec(ctx, c.cfg.GitHubBin, []string{"api", "graphql", "-f", "query=" + githubQuery, "-f", "owner=" + parts[0], "-f", "repo=" + parts[1], "-F", "number=" + strconv.FormatInt(ref.Number, 10)})
	var response struct {
		Data struct {
			Repository *struct {
				PullRequest *struct {
					Number                                                int64 `json:"number"`
					URL, State, HeadRefOid, UpdatedAt, ClosedAt, MergedAt string
					MergeCommit                                           *struct {
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
	out := Observation{State: state, HeadSHA: pr.HeadRefOid, URL: pr.URL, UpdatedAt: pr.UpdatedAt, MergedAt: pr.MergedAt, ClosedAt: pr.ClosedAt, MergedBy: identity(pr.MergedBy)}
	if pr.MergeCommit != nil {
		out.MergeSHA = pr.MergeCommit.OID
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
