package cli

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// issuePullRequestFromTask uses the workspace's first recorded branch. Its
// repository and branch come from the ledger and never from the current HEAD.
func issuePullRequestFromTask(ctx context.Context, c *client, issueID, taskID, mount string) (string, error) {
	var task protocol.TaskDetail
	if err := c.do(ctx, "GET", "/api/v1/tasks/"+url.PathEscape(taskID), nil, &task); err != nil {
		return "", err
	}
	ws := task.WorkspaceState
	if task.PrimaryIssueID != issueID || ws == nil || ws.OwnerWorkItemID != issueID {
		return "", usagef("task needs a recorded workspace owned by this Issue; use --pr-url otherwise")
	}
	index := slices.IndexFunc(ws.Mounts, func(m protocol.MountView) bool { return m.Path == mount })
	if index < 0 || ws.Mounts[index].Branch == "" || ws.Mounts[index].ResolvedBaseSHA == "" {
		return "", usagef("mount needs its original recorded workspace branch; use --pr-url otherwise")
	}
	source := ws.Mounts[index]
	var repo protocol.RepositoryRegistration
	if err := c.do(ctx, "GET", "/api/v1/repos/"+url.PathEscape(source.Repo), nil, &repo); err != nil {
		return "", err
	}
	repository := githubRepositoryFromRemote(repo.RemoteURL)
	if repo.Retired || repository == "" || slices.Contains(repo.BaseCandidates, source.Branch) {
		return "", usagef("mount needs a registered GitHub repository and its original work branch; use --pr-url otherwise")
	}
	link, err := findGitHubBranchPR(ctx, repository, source.Branch, runGitHubBranchQuery)
	if err != nil {
		return "", fmt.Errorf("could not confirm a unique PR from the recorded branch, use --pr-url: %w", err)
	}
	return link, nil
}

func githubRepositoryFromRemote(remote string) string {
	if path, ok := strings.CutPrefix(remote, "git@github.com:"); ok {
		remote = "ssh://git@github.com/" + path
	}
	u, err := url.Parse(remote)
	if err != nil || !strings.EqualFold(u.Host, "github.com") || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" ||
		!(u.Scheme == "https" && u.User == nil || u.Scheme == "ssh" && u.User.String() == "git") {
		return ""
	}
	repo := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if protocol.GitHubPullRequestURL(repo, 1) == "" {
		return ""
	}
	return repo
}
