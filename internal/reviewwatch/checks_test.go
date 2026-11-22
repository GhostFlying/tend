package reviewwatch

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
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
				return json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"number": 7, "state": "OPEN", "headRefOid": "head", "mergeable": "MERGEABLE", "commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": tc.commit, "statusCheckRollup": map[string]any{"state": "SUCCESS", "contexts": map[string]any{"totalCount": tc.total, "nodes": []any{map[string]any{"name": "required", "isRequired": true, "status": "COMPLETED", "conclusion": "SUCCESS"}, map[string]any{"name": "conditional", "isRequired": false, "status": "COMPLETED", "conclusion": "SKIPPED"}}}}}}}}}}}})
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
			if got.CI != nil {
				required, optional := got.CI.Checks[0].Required, got.CI.Checks[1].Required
				if required == nil || !*required || optional == nil || *optional {
					t.Fatal("missing provider classification", got.CI.Checks)
				}
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
	c := New(Config{IncludeChecks: true, Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[3] == "get" {
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

func TestCodebaseNamedChecksPreserveRunnerCandidateBase(t *testing.T) {
	c := New(Config{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[3] == "get" {
			return []byte(`{"status":"success","data":{"merge_request":{"Number":7,"Status":"open","TargetBranch":{"Name":"integration","Commit":{"Id":"live-target"}},"Versions":[{"Number":1,"SourceCommitId":"head","TargetCommitId":"patchset-base"}]}}}`), nil
		}
		return []byte(`{"status":"success","data":{"merge_request":{"number":7,"status":"open","source_commit_id":"head"},"check_runs":{"total_count":1,"items":[{"Name":"required","Status":"completed","Conclusion":"success"}]}}}`), nil
	}})
	ref := Ref{Provider: Codebase, Repository: "example/project", Number: 7}
	before, err := c.Observe(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	ref.IncludeChecks = true
	during, err := c.Observe(t.Context(), ref)
	if err != nil || during.CI == nil || len(during.CI.Checks) != 1 || during.BaseSHA != "live-target" ||
		before.BaseSHA != during.BaseSHA || before.HeadSHA != during.HeadSHA || before.TargetSHA != during.TargetSHA {
		t.Fatal("requesting check evidence changed existing candidate metadata", before, during, err)
	}
}

func TestCodebaseExplicitChecksKeepSeparateMergeLineage(t *testing.T) {
	c := New(Config{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[2] == "commit" {
			return []byte(`{"status":"success","data":{"ref_name":"target..merge","page_number":1,"commits":[]}}`), nil
		}
		if args[3] == "get" {
			return []byte(`{"status":"success","data":{"merge_request":{"Number":7,"Status":"merged","MergeCommitId":"merge","TargetBranch":{"Name":"integration","Commit":{"Id":"target"}},"Versions":[{"Number":1,"SourceCommitId":"head","TargetCommitId":"base"}]}}}`), nil
		}
		return []byte(`{"status":"success","data":{"merge_request":{"number":7,"status":"merged","source_commit_id":"head"},"check_runs":{"total_count":1,"items":[{"Name":"required","Status":"completed","Conclusion":"success"}]}}}`), nil
	}})
	o, err := c.Observe(t.Context(), Ref{Provider: Codebase, Repository: "example/project", Number: 7, IncludeChecks: true})
	if err != nil || !o.TargetContainsMerge || o.LineageErrorKind != "" || o.BaseSHA != "target" ||
		o.CI == nil || len(o.CI.Checks) != 1 {
		t.Fatal("check enrichment lost the independent lineage proof or changed legacy base", o, err)
	}
}

func TestCodebaseNamedCheckReadSurvivesIndependentLineageFailure(t *testing.T) {
	for _, server := range []bool{true, false} {
		t.Run(map[bool]string{true: "server", false: "runner"}[server], func(t *testing.T) {
			statusRead := make(chan struct{})
			c := New(Config{IncludeChecks: server, Timeout: 5 * time.Second, Exec: func(ctx context.Context, _ string, args []string) ([]byte, error) {
				switch {
				case args[2] == "commit":
					select {
					case <-statusRead:
						return nil, errors.New("merge lineage service is unavailable")
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				case args[3] == "get":
					return []byte(`{"status":"success","data":{"merge_request":{"Number":7,"Status":"merged","MergeCommitId":"merge","TargetBranch":{"Name":"integration","Commit":{"Id":"target"}},"Versions":[{"Number":1,"SourceCommitId":"head","TargetCommitId":"base"}]}}}`), nil
				case args[3] == "status":
					close(statusRead)
					return []byte(`{"status":"success","data":{"merge_request":{"number":7,"status":"merged","source_commit_id":"head"},"check_runs":{"total_count":1,"items":[{"Name":"required","Status":"completed","Conclusion":"success"}]}}}`), nil
				default:
					return nil, errors.New("unexpected provider operation")
				}
			}})
			o, err := c.Observe(t.Context(), Ref{Provider: Codebase, Repository: "example/project", Number: 7, IncludeChecks: !server})
			expectedBase := map[bool]string{true: "base", false: "target"}[server]
			if err != nil || o.TargetContainsMerge || o.LineageErrorKind != "query_failed" || o.BaseSHA != expectedBase ||
				o.CI == nil || o.CI.ErrorKind != "" || len(o.CI.Checks) != 1 || o.CI.Checks[0].Conclusion != "success" {
				t.Fatal("lineage failure held the separately observed named check", o, err)
			}
		})
	}
}
