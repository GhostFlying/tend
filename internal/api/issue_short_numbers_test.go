package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueShortNumbersThroughOwnerHTTP(t *testing.T) {
	e := newEnv(t)
	e.configureProjects()
	token := e.login(protocol.ScopeOwner)
	create := func(project, title string) protocol.Envelope {
		return e.envelope("issues.create", protocol.Target{Type: "issue"}, nil, protocol.CreateIssuePayload{
			Plan: protocol.IssuePlan{Title: title, Project: project, Intent: "Verify permanent Issue numbers", Stages: []protocol.IssueStage{}},
		})
	}
	original := create("web", "Release step search")
	first := issueHTTP(t, e, token, original)
	bare, _ := protocol.IssueNumberInput(first.ShortCode, false)
	if bare == "" {
		t.Fatalf("create result has no short code: %+v", first)
	}
	const concurrent = 10
	var wg sync.WaitGroup
	issues := make(chan protocol.Issue, concurrent+1)
	for i := range concurrent {
		env := create([]string{"core", "web"}[i%2], fmt.Sprintf("Concurrent %d", i))
		wg.Go(func() { issues <- issueHTTP(t, e, token, env) })
	}
	wg.Go(func() {
		retry := issueHTTP(t, e, token, original)
		if retry.IssueID != first.IssueID || retry.ShortCode != first.ShortCode {
			t.Errorf("retry replaced the original identity: %+v", retry)
		}
	})
	wg.Wait()
	close(issues)
	seen := map[string]bool{first.ShortCode: true}
	for issue := range issues {
		n, err := strconv.ParseInt(strings.TrimPrefix(issue.ShortCode, "T"), 10, 64)
		firstNumber, _ := strconv.ParseInt(bare, 10, 64)
		if err != nil || n <= firstNumber || seen[issue.ShortCode] {
			t.Fatalf("non-unique or non-increasing short number: %+v", issue)
		}
		seen[issue.ShortCode] = true
	}
	if len(seen) != concurrent+1 {
		t.Fatalf("short numbers = %v", seen)
	}
	for _, ref := range []string{first.IssueID, first.ShortCode, strings.ToLower(first.ShortCode), bare} {
		view := issueDetailHTTP(t, e, token, ref)
		if view.IssueID != first.IssueID || view.ShortCode != first.ShortCode {
			t.Fatalf("detail lookup %s = %+v", ref, view)
		}
	}
	var identity protocol.IssueIdentity
	if status := e.do("GET", "/api/v1/issues/"+first.ShortCode+"?projection=identity", token, nil, &identity); status != http.StatusOK || identity.IssueID != first.IssueID || identity.Title != first.Plan.Title {
		t.Fatalf("identity HTTP%d %+v", status, identity)
	}
	for _, term := range []string{bare, "step+search", strings.ToLower(first.ShortCode)} {
		var page protocol.IssuePage
		status := e.do("GET", "/api/v1/issues?project=web&search="+term, token, nil, &page)
		found := false
		for _, issue := range page.Items {
			found = found || (issue.IssueID == first.IssueID && issue.ShortCode == first.ShortCode)
			if issue.Project != "web" {
				t.Fatalf("project lookup leaked %+v", issue)
			}
		}
		if status != http.StatusOK || !found || (term == bare && len(page.Items) != 1) {
			t.Fatalf("search %s HTTP%d %+v", term, status, page)
		}
	}
}
