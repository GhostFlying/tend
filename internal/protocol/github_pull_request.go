package protocol

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)
var githubPullRequestPath = regexp.MustCompile(`^/([A-Za-z0-9-]+/[A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)/?$`)

// GitHubPullRequestURL constructs a link for the GitHub.com observer's identity.
func GitHubPullRequestURL(repository string, number int64) string {
	if !githubRepositoryPattern.MatchString(repository) || number < 1 {
		return ""
	}
	_, name, _ := strings.Cut(repository, "/")
	if name == "." || name == ".." {
		return ""
	}
	return "https://github.com/" + repository + "/pull/" + strconv.FormatInt(number, 10)
}

// ParseGitHubPullRequestURL accepts a URL from GitHub's PR page, including
// optional browser navigation parameters. Store only the canonical link.
func ParseGitHubPullRequestURL(raw string) (IssueDelivery, error) {
	invalid := Errorf(CodeInvalidRequest, "expected an HTTPS GitHub.com pull request URL")
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawPath != "" {
		return IssueDelivery{}, invalid
	}
	match := githubPullRequestPath.FindStringSubmatch(u.Path)
	if match == nil {
		return IssueDelivery{}, invalid
	}
	number, err := strconv.ParseInt(match[2], 10, 64)
	link := GitHubPullRequestURL(match[1], number)
	if err != nil || link == "" {
		return IssueDelivery{}, invalid
	}
	return IssueDelivery{Provider: "github", Repository: match[1], Number: number, URL: link}, nil
}
