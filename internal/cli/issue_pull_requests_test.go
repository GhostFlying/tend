package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueRegisterPullRequestDryRun(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/issues/issue", protocol.IssueView{Issue: protocol.Issue{
		IssueControl: protocol.IssueControl{IssueID: "issue", AssignmentEpoch: 3},
		Revision:     7,
	}})
	link := "https://github.com/acme/repo/pull/44"
	if code := e.main("issue", "update-delivery", "issue", "--pr-url", link, "--reason", "Attach PR", "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var envelope protocol.Envelope
	var p protocol.UpdateIssueDeliveryPayload
	if json.Unmarshal(e.out.Bytes(), &envelope) != nil || json.Unmarshal(envelope.Payload, &p) != nil ||
		envelope.Operation != "issues.update_delivery" || envelope.Target.ID != "issue" ||
		envelope.ExpectedControlRevision == nil || *envelope.ExpectedControlRevision != 7 ||
		p.Action != "register_pr" || p.PRURL != link || p.ExpectedAssignmentEpoch != 3 ||
		len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal(envelope, p)
	}
}

func TestFindGitHubBranchPRRequiresOneExactRepositoryAndBranch(t *testing.T) {
	const exact = `{"number":44,"html_url":"https://github.com/acme/repo/pull/44","head":{"ref":"task/old-branch","repo":{"full_name":"acme/repo"}},"base":{"repo":{"full_name":"acme/repo"}}}`
	for _, test := range []struct{ name, payload, err string }{
		{"unique", "[" + exact + "]", ""},
		{"none", "[]", "no matching PR"},
		{"ambiguous", "[" + exact + "," + exact + "]", "multiple matching PRs"},
		{"switched", "[" + strings.Replace(exact, "old-branch", "new-branch", 1) + "]", "identity differs"},
		{"fork", "[" + strings.Replace(exact, `"full_name":"acme/repo"`, `"full_name":"acme/fork"`, 1) + "]", "identity differs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			link, err := findGitHubBranchPR(t.Context(), "acme/repo", "task/old-branch", func(ctx context.Context, args ...string) ([]byte, error) {
				calls++
				if _, deadline := ctx.Deadline(); !deadline || !slices.Contains(args, "head=acme:task/old-branch") || !slices.Contains(args, "per_page=2") || slices.Contains(args, "--paginate") {
					t.Fatal("unbounded or wrong branch lookup", args)
				}
				return []byte(test.payload), nil
			})
			if calls != 1 {
				t.Fatal(calls)
			}
			if test.err == "" && (err != nil || link != "https://github.com/acme/repo/pull/44") {
				t.Fatal(link, err)
			}
			if test.err != "" && (err == nil || !strings.Contains(err.Error(), test.err) || link != "") {
				t.Fatal(link, err)
			}
		})
	}
}

func TestIssuePullRequestLookupRequiresTheOriginalGitOrigin(t *testing.T) {
	const remote = "https://github.com/acme/repo.git"
	for _, tc := range []struct {
		name, identity string
		query          bool
	}{
		{name: "original", identity: protocol.RepositoryRemoteKey(remote), query: true},
		{name: "old record without origin"},
		{name: "different Orca origin", identity: protocol.RepositoryRemoteKey("https://github.com/acme/fork.git")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			f.answer("GET /api/v1/tasks/task", protocol.TaskDetail{
				TaskSummary: protocol.TaskSummary{PrimaryIssueID: "issue"},
				WorkspaceState: &protocol.WorkspaceView{OwnerWorkItemID: "issue", Mounts: []protocol.MountView{{
					Path: "repo", Repo: "acme-repo", Branch: "work/first", ResolvedBaseSHA: strings.Repeat("a", 40), OriginIdentity: tc.identity,
				}}},
			})
			f.answer("GET /api/v1/repos/acme-repo", protocol.RepositoryRegistration{Repo: "acme-repo", RemoteURL: remote})
			client, err := e.client(protocol.ScopeOwner)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			link, err := issuePullRequestFromTask(t.Context(), client, "issue", "task", "repo", func(_ context.Context, args ...string) ([]byte, error) {
				called = true
				if !slices.Contains(args, "head=acme:work/first") {
					t.Fatal(args)
				}
				return []byte(`[{"number":44,"html_url":"https://github.com/acme/repo/pull/44","head":{"ref":"work/first","repo":{"full_name":"acme/repo"}},"base":{"repo":{"full_name":"acme/repo"}}}]`), nil
			})
			if called != tc.query {
				t.Fatal("provider queried with missing or different origin evidence", called)
			}
			if tc.query && (err != nil || link != "https://github.com/acme/repo/pull/44") ||
				!tc.query && (err == nil || !strings.Contains(err.Error(), "--pr-url") || link != "") {
				t.Fatal(link, err)
			}
		})
	}
}
