package reviewwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

type githubCheckContext struct {
	ID, Name, Status, Conclusion, DetailsURL, Context, State, TargetURL string
	IsRequired                                                          *bool
	CheckSuite                                                          *struct {
		App         *struct{ Slug string }
		WorkflowRun *struct{ URL string }
	}
}

func (check githubCheckContext) normalize(repo, prURL string) protocol.DeliveryCheck {
	if check.Name == "" {
		return protocol.DeliveryCheck{ID: check.ID, Required: check.IsRequired, Source: "github_status_context",
			Name: check.Context, State: strings.ToLower(check.State), URL: check.TargetURL}
	}
	out := protocol.DeliveryCheck{ID: check.ID, Required: check.IsRequired, Source: "github_check_run",
		Name: check.Name, State: strings.ToLower(check.Status), Conclusion: strings.ToLower(check.Conclusion), URL: check.DetailsURL}
	if suite := check.CheckSuite; suite != nil {
		if suite.App != nil {
			out.App = suite.App.Slug
		}
		if out.App == "github-actions" && suite.WorkflowRun != nil {
			out.WorkflowRunID = githubWorkflowRunID(repo, prURL, suite.WorkflowRun.URL)
		}
	}
	return out
}

func githubWorkflowRunID(repo, prURL, runURL string) int64 {
	pr, err := url.Parse(prURL)
	if err != nil {
		return 0
	}
	run, err := url.Parse(runURL)
	if err != nil || run.Scheme != "https" || run.Host == "" || run.Host != pr.Host {
		return 0
	}
	repoPath := "/" + repo + "/actions/runs/"
	if !strings.HasPrefix(strings.ToLower(run.Path), strings.ToLower(repoPath)) {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSuffix(run.Path[len(repoPath):], "/"), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

type githubAttemptJob struct {
	NodeID     string `json:"node_id"`
	RunID      int64  `json:"run_id"`
	HeadSHA    string `json:"head_sha"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

// An explicitly pinned attempt stays addressable after GitHub replaces its
// checks in the commit rollup. Keep this evidence out of the current check list.
func (c *Client) githubPinnedChecks(ctx context.Context, ref Ref, head string) []protocol.DeliveryCheck {
	type workflowAttempt struct {
		run     int64
		attempt int
	}
	jobsByAttempt := map[workflowAttempt][]githubAttemptJob{}
	var matches []protocol.DeliveryCheck
	for _, requested := range ref.CheckAttempts {
		if ctx.Err() != nil {
			break
		}
		if requested.HeadSHA != head || head == "" || requested.WorkflowRunID <= 0 || requested.Attempt <= 0 {
			continue
		}
		key := workflowAttempt{requested.WorkflowRunID, requested.Attempt}
		jobs, read := jobsByAttempt[key]
		if !read {
			jobs = c.githubAttemptJobs(ctx, ref.Repository, key.run, key.attempt)
			jobsByAttempt[key] = jobs
		}
		for _, job := range jobs {
			if job.NodeID == "" || job.Name != requested.Name || job.RunID != key.run || job.HeadSHA != head {
				continue
			}
			matches = append(matches, protocol.DeliveryCheck{ID: job.NodeID, Name: job.Name,
				Source: "github_check_run", App: "github-actions", WorkflowRunID: key.run, Attempt: key.attempt,
				State: strings.ToLower(job.Status), Conclusion: strings.ToLower(job.Conclusion), URL: job.HTMLURL})
		}
	}
	return matches
}

func (c *Client) githubAttemptJobs(ctx context.Context, repo string, run int64, attempt int) []githubAttemptJob {
	path := fmt.Sprintf("repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", repo, run, attempt)
	raw, err := c.cfg.Exec(ctx, c.cfg.GitHubBin, []string{"api", path})
	if err != nil {
		return nil
	}
	var page struct {
		Jobs       []githubAttemptJob
		TotalCount int `json:"total_count"`
	}
	if json.Unmarshal(raw, &page) != nil || page.TotalCount != len(page.Jobs) {
		return nil
	}
	return page.Jobs
}
