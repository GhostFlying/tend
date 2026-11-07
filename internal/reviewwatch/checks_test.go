package reviewwatch

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestGitHubChecksAreBoundToHeadAndKeepSkippedDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, commit string
		total        int
		wantCI       bool
		state        string
	}{{"exact", "head", 2, true, "success"}, {"different candidate", "older", 2, false, ""}, {"truncated", "head", 101, true, "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			client := New(Config{IncludeChecks: true, Exec: func(context.Context, string, []string) ([]byte, error) {
				return json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"number": 7, "state": "OPEN", "headRefOid": "head", "mergeable": "MERGEABLE", "commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": tc.commit, "statusCheckRollup": map[string]any{"state": "SUCCESS", "contexts": map[string]any{"totalCount": tc.total, "nodes": []any{map[string]any{"name": "required", "status": "COMPLETED", "conclusion": "SUCCESS"}, map[string]any{"name": "conditional", "status": "COMPLETED", "conclusion": "SKIPPED"}}}}}}}}}}}})
			}})
			got, err := client.Observe(t.Context(), Ref{Provider: GitHub, Repository: "example/project", Number: 7})
			if err != nil {
				t.Fatal(err)
			}
			if got.Mergeability != "mergeable" || (got.CI != nil) != tc.wantCI {
				t.Fatal(got)
			}
			if got.CI != nil && (got.CI.State != tc.state || got.CI.HeadSHA != "head" || got.CI.Checks[1].Conclusion != "skipped") {
				t.Fatal(got.CI)
			}
		})
	}
}

func TestCodebaseChecksUseNativeStatusAndRejectHeadDrift(t *testing.T) {
	for _, head := range []string{"head", "other"} {
		t.Run(head, func(t *testing.T) {
			calls := 0
			client := New(Config{IncludeChecks: true, Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
				calls++
				if calls == 1 {
					return []byte(`{"status":"success","data":{"merge_request":{"Number":7,"Status":"open","Versions":[{"Number":1,"SourceCommitId":"head"}]}}}`), nil
				}
				if !strings.Contains(strings.Join(args, " "), "mr status 7 -R example/project --page-size 100") {
					t.Fatal(args)
				}
				return json.Marshal(map[string]any{"status": "success", "data": map[string]any{"merge_request": map[string]any{"number": 7, "status": "open", "source_commit_id": head}, "mergeability": map[string]any{"mergeable": true}, "check_runs": map[string]any{"total_count": 2, "items": []any{map[string]any{"Name": "required", "Status": "completed", "Conclusion": "success"}, map[string]any{"Name": "conditional", "Status": "completed", "Conclusion": "skipped"}}}}})
			}})
			got, err := client.Observe(t.Context(), Ref{Provider: Codebase, Repository: "example/project", Number: 7})
			if head == "other" {
				if err != nil || got.State != "open" || got.CI == nil || got.CI.State != "unknown" || got.CI.ErrorKind != "query_failed" || got.CI.HeadSHA != "head" {
					t.Fatal("combined different candidates or lost PR facts", got, err)
				}
				return
			}
			if err != nil || got.Mergeability != "mergeable" || got.CI == nil || got.CI.State != "success" || got.CI.Checks[1].Conclusion != "skipped" {
				t.Fatal(got, err)
			}
		})
	}
}

func TestProviderErrorsHaveSafeActionableCategories(t *testing.T) {
	for _, tc := range []struct{ stderr, kind string }{{"HTTP 429 rate limit and private output", "rate_limited"}, {"run gh auth login; private output", "authentication_required"}, {"could not resolve host: private output", "unreachable"}} {
		err := commandError(&exec.ExitError{Stderr: []byte(tc.stderr)})
		var failure *Error
		if !errors.As(err, &failure) || failure.Kind != tc.kind || strings.Contains(err.Error(), "private") {
			t.Fatal(err)
		}
	}
}

func TestCodebaseServerKeepsFrozenBaseApartFromLiveTarget(t *testing.T) {
	calls := 0
	c := New(Config{IncludeChecks: true, Exec: func(context.Context, string, []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"status":"success","data":{"merge_request":{"Number":7,"Status":"merged","MergeCommitId":"merge","TargetBranch":{"Commit":{"Id":"advanced-target"}},"Versions":[{"Number":1,"SourceCommitId":"head","TargetCommitId":"frozen-base"}]}}}`), nil
		}
		return nil, errors.New("provider status unavailable")
	}})
	got, err := c.Observe(t.Context(), Ref{Provider: Codebase, Repository: "example/project", Number: 7})
	if err != nil || got.State != "merged" || got.MergeSHA != "merge" || got.BaseSHA != "frozen-base" || got.TargetSHA != "advanced-target" || got.CI == nil || got.CI.State != "unknown" || got.CI.ErrorKind != "query_failed" {
		t.Fatal(got, err)
	}
}

func TestCodebaseServerProvesAdvancedTargetLineage(t *testing.T) {
	for _, tc := range []struct {
		name, rangeJSON string
		confirmed       bool
	}{{"integrated", `{"status":"success","data":{"ref_name":"target..merge","page_number":1,"commits":[]}}`, true}, {"diverged", `{"status":"success","data":{"ref_name":"target..merge","page_number":1,"commits":[{"Id":"missing-commit"}]}}`, false}, {"missing array", `{"status":"success","data":{"ref_name":"target..merge","page_number":1}}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			client := New(Config{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if strings.Join(args, " ") != "--json codebase commit list -R example/project --base target --target merge --page 1 --page-size 1" {
					t.Fatal(args)
				}
				return []byte(tc.rangeJSON), nil
			}})
			got, err := client.codebaseLineage(t.Context(), Ref{Repository: "example/project"}, "target", "merge")
			if got != tc.confirmed || (tc.name == "missing array") != (err != nil) {
				t.Fatal(got, err)
			}
		})
	}
}

func TestGitHubServerRetainsMergedFactsWhenLineageReadFails(t *testing.T) {
	calls := 0
	c := New(Config{IncludeChecks: true, Exec: func(context.Context, string, []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"data":{"repository":{"pullRequest":{"number":7,"state":"MERGED","headRefOid":"head","baseRef":{"target":{"oid":"target"}},"mergeCommit":{"oid":"merge"}}}}}`), nil
		}
		return nil, &exec.ExitError{Stderr: []byte("HTTP 429")}
	}})
	got, err := c.Observe(t.Context(), Ref{Provider: GitHub, Repository: "example/project", Number: 7})
	if err != nil || got.State != "merged" || got.MergeSHA != "merge" || got.LineageErrorKind != "rate_limited" || got.TargetContainsMerge {
		t.Fatal(got, err)
	}
}

func TestGitHubLegacyQueryDoesNotRequestChecks(t *testing.T) {
	c := New(Config{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "statusCheckRollup") {
			t.Fatal("legacy default requested new fields", args)
		}
		return []byte(`{"data":{"repository":{"pullRequest":{"number":7,"state":"OPEN"}}}}`), nil
	}})
	if _, err := c.Observe(t.Context(), Ref{Provider: GitHub, Repository: "example/project", Number: 7}); err != nil {
		t.Fatal(err)
	}
}
