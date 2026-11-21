package reviewwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestGitHubWaitEvidenceVerifiesExactAttemptJob(t *testing.T) {
	ref := Ref{Provider: GitHub, Repository: "example/project", Number: 7}
	const workflow = 76231873298
	const latestRollup = `{"data":{"repository":{"pullRequest":{"number":7,"url":"https://github.com/example/project/pull/7","state":"OPEN","headRefOid":"head","baseRefOid":"base","baseRefName":"integration","baseRef":{"target":{"oid":"target"}},"commits":{"nodes":[{"commit":{"oid":"head","statusCheckRollup":{"state":"PENDING","contexts":{"totalCount":1,"nodes":[{"id":"check-current","name":"integration-check","status":"IN_PROGRESS","conclusion":null,"detailsUrl":"https://github.com/example/project/actions/runs/76231873298/job/42","checkSuite":{"app":{"slug":"github-actions"},"workflowRun":{"url":"https://github.com/example/project/actions/runs/76231873298"}}}]}}}}]}}}}}`
	for _, tc := range []struct {
		name    string
		attempt int
		jobs    string
		wantJob string
	}{
		{"ordinary rollup reads no attempt jobs", 0, "", ""},
		{"old attempt hidden behind current rerun", 2, `{"total_count":1,"jobs":[{"node_id":"check-old","run_id":76231873298,"head_sha":"head","name":"integration-check","status":"completed","conclusion":"failure","html_url":"https://github.com/example/project/actions/runs/76231873298/job/41"}]}`, "check-old"},
		{"misbound head", 2, `{"total_count":1,"jobs":[{"node_id":"check-old","run_id":76231873298,"head_sha":"wrong","name":"integration-check"}]}`, ""},
		{"misbound workflow", 2, `{"total_count":1,"jobs":[{"node_id":"check-old","run_id":76231873297,"head_sha":"head","name":"integration-check"}]}`, ""},
		{"truncated attempt", 2, `{"total_count":2,"jobs":[{"node_id":"check-old","run_id":76231873298,"head_sha":"head","name":"integration-check"}]}`, ""},
		{"provider no attempt evidence", 2, `{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attemptReads := 0
			cfg := Config{IncludeChecks: true, Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if len(args) > 1 && args[1] == "graphql" {
					if !strings.Contains(args[3], "checkSuite{app{slug} workflowRun{url}}") {
						t.Fatal("workflow and app identity are absent from the native query", args)
					}
					return []byte(latestRollup), nil
				}
				if tc.attempt == 0 || len(args) != 2 || args[1] != fmt.Sprintf("repos/example/project/actions/runs/%d/attempts/%d/jobs?per_page=100", workflow, tc.attempt) {
					t.Fatal("unexpected extra provider read", args)
				}
				attemptReads++
				return []byte(tc.jobs), nil
			}}
			query := ref
			if tc.attempt > 0 {
				query.CheckAttempts = []protocol.PRCheckAttempt{{HeadSHA: "head", Name: "integration-check", WorkflowRunID: workflow, Attempt: tc.attempt}}
			}
			o, err := New(cfg).Observe(t.Context(), query)
			if err != nil || o.TargetRef != "integration" || o.CI == nil || len(o.CI.Checks) != 1 {
				t.Fatal(o, err)
			}
			current := o.CI.Checks[0]
			if current.ID != "check-current" || current.App != "github-actions" || current.WorkflowRunID != workflow ||
				current.Attempt != 0 || current.State != "in_progress" || attemptReads > 1 || tc.attempt > 0 && attemptReads != 1 {
				t.Fatal("current commit rollup was replaced by historical attempt", current, attemptReads)
			}
			if tc.wantJob == "" {
				if len(o.CI.AttemptChecks) != 0 {
					t.Fatal("unproven attempt returned", o.CI.AttemptChecks)
				}
				return
			}
			if len(o.CI.AttemptChecks) != 1 {
				t.Fatal("requested independent attempt result missing", o.CI)
			}
			check := o.CI.AttemptChecks[0]
			if check.ID != tc.wantJob || check.App != "github-actions" || check.WorkflowRunID != workflow ||
				check.Attempt != tc.attempt || check.Conclusion != "failure" || check.State != "completed" || check.URL == "" {
				t.Fatal("incorrect provider proof", check)
			}
		})
	}
}

func TestGitHubRunnerPRMetadataSurvivesIndependentLineageFailure(t *testing.T) {
	ref := Ref{Provider: GitHub, Repository: "example/project", Number: 7, PreservePartial: true}
	cfg := Config{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if len(args) > 1 && args[1] == "graphql" {
			if strings.Contains(args[3], "statusCheckRollup") {
				t.Fatal("metadata-only runner unexpectedly read checks")
			}
			return []byte(`{"data":{"repository":{"pullRequest":{"number":7,"url":"https://github.com/example/project/pull/7","state":"MERGED","headRefOid":"head","baseRefOid":"base","baseRefName":"integration","baseRef":{"target":{"oid":"advanced"}},"mergeCommit":{"oid":"merged"}}}}}`), nil
		}
		if len(args) != 2 || args[1] != "repos/example/project/compare/merged...advanced" {
			t.Fatal("unexpected provider operation", args)
		}
		return nil, errors.New("separate comparison is unavailable")
	}}
	out, err := New(cfg).Observe(t.Context(), ref)
	if err != nil || out.State != Merged || out.TargetRef != "integration" || out.TargetSHA != "advanced" ||
		out.LineageErrorKind != "query_failed" || out.TargetContainsMerge || out.CI != nil {
		t.Fatal("provider state was discarded or treated as lineage proof", out, err)
	}
	ref.PreservePartial = false
	if out, err := New(cfg).Observe(t.Context(), ref); err == nil || out.State != "" {
		t.Fatal("legacy observer changed its expected result on partial failure", out, err)
	}
}

func TestGitHubPinnedAttemptReadIsIndependentOfMergeLineage(t *testing.T) {
	attempt := protocol.PRCheckAttempt{HeadSHA: "head", Name: "integration-check", WorkflowRunID: 7, Attempt: 2}
	ref := Ref{Provider: GitHub, Repository: "example/project", Number: 7, CheckAttempts: []protocol.PRCheckAttempt{attempt}}
	attemptRead := make(chan struct{})
	cfg := Config{Exec: func(ctx context.Context, _ string, args []string) ([]byte, error) {
		switch args[1] {
		case "graphql":
			return []byte(`{"data":{"repository":{"pullRequest":{"number":7,"url":"https://github.com/example/project/pull/7","state":"MERGED","headRefOid":"head","baseRefOid":"base","baseRefName":"integration","baseRef":{"target":{"oid":"advanced"}},"mergeCommit":{"oid":"merged"}}}}}`), nil
		case "repos/example/project/compare/merged...advanced":
			select {
			case <-attemptRead:
				return nil, errors.New("lineage unavailable independently of the completed attempt")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case "repos/example/project/actions/runs/7/attempts/2/jobs?per_page=100":
			close(attemptRead)
			return []byte(`{"total_count":1,"jobs":[{"node_id":"old-check","run_id":7,"head_sha":"head","name":"integration-check","status":"completed","conclusion":"success"}]}`), nil
		default:
			return nil, errors.New("unexpected provider operation")
		}
	}}
	o, err := New(cfg).Observe(t.Context(), ref)
	if err != nil || o.TargetContainsMerge || o.CI == nil || len(o.CI.AttemptChecks) != 1 || o.CI.AttemptChecks[0].ID != "old-check" {
		t.Fatal("separate lineage operation held the explicit attempt proof", o, err)
	}
}
