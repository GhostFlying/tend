package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// findGitHubBranchPR makes a single bounded lookup for a known source branch
// in its own repository. Any ambiguity requires the explicit PR URL.
func findGitHubBranchPR(ctx context.Context, repository, branch string, query func(context.Context, ...string) ([]byte, error)) (string, error) {
	owner, _, _ := strings.Cut(repository, "/")
	if protocol.GitHubPullRequestURL(repository, 1) == "" || branch == "" {
		return "", errors.New("invalid GitHub branch reference")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, err := query(ctx,
		"api", "--hostname", "github.com", "--method", "GET", "repos/"+repository+"/pulls",
		"-f", "head="+owner+":"+branch, "-f", "state=all", "-F", "per_page=2",
	)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", errors.New("GitHub query failed")
	}
	type gitRef struct {
		Ref  string
		Repo *struct {
			FullName string `json:"full_name"`
		}
	}
	var prs []struct {
		Number int64
		URL    string `json:"html_url"`
		Head   gitRef
		Base   gitRef
	}
	if json.Unmarshal(body, &prs) != nil {
		return "", errors.New("invalid GitHub response")
	}
	switch len(prs) {
	case 0:
		return "", errors.New("no matching PR")
	case 1:
		pr := prs[0]
		urlRef, err := protocol.ParseGitHubPullRequestURL(pr.URL)
		if err != nil || urlRef.Number != pr.Number || !strings.EqualFold(urlRef.Repository, repository) ||
			pr.Head.Ref != branch || pr.Head.Repo == nil || !strings.EqualFold(pr.Head.Repo.FullName, repository) ||
			pr.Base.Repo == nil || !strings.EqualFold(pr.Base.Repo.FullName, repository) {
			return "", errors.New("PR identity differs from the recorded work branch")
		}
		return urlRef.URL, nil
	default:
		return "", errors.New("multiple matching PRs")
	}
}

func runGitHubBranchQuery(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "gh", args...).Output()
}
