package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueMaterialsCLIProjectDiscoveryPagesAndSelectedIssue(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	pages := map[string]protocol.IssueMaterials{
		"":          {Kind: "issues", IssueIDs: []string{"related-a"}, More: true, Next: "related-a"},
		"related-a": {Kind: "issues", IssueIDs: []string{"related-b"}},
	}
	for cursor, page := range pages {
		page.Issues = []protocol.IssueView{{Issue: protocol.Issue{IssueControl: protocol.IssueControl{IssueID: page.IssueIDs[0]}, Plan: protocol.IssuePlan{Title: "Related project knowledge"}}}}
		pages[cursor] = page
	}
	f.on("GET /api/v1/issues/original/materials", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		page, ok := pages[r.URL.Query().Get("after")]
		if !ok || r.URL.Query().Get("kind") != "issues" || r.URL.Query().Get("limit") != "1" {
			t.Error("project discovery changed its page selector", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	for _, cursor := range []string{"", "related-a"} {
		e.out.Reset()
		args := []string{"issue", "materials", "original", "--kind", "issues", "--limit", "1", "--json"}
		if cursor != "" {
			args = append(args, "--after", cursor)
		}
		if code := e.main(args...); code != exitOK {
			t.Fatal(code, e.errOut.String())
		}
		var got protocol.IssueMaterials
		if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, pages[cursor]) {
			t.Fatal("discovery lost object identity or pagination", got, err)
		}
	}
	want := protocol.IssueMaterials{Kind: "overview", IssueIDs: []string{"related-b"}}
	f.on("GET /api/v1/issues/related-b/materials", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("kind") != "overview" {
			t.Error("selected Issue was replaced with a project-wide read")
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	e.out.Reset()
	if code := e.main("issue", "materials", "related-b", "--kind", "overview", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var got protocol.IssueMaterials
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) || len(f.requests) != 3 {
		t.Fatal("selected Issue read changed scope or performed extra IO", got, f.requests, err)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("discovery or reading created execution authority")
	}
}
