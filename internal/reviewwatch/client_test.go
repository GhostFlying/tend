package reviewwatch

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGitHubObservationAndCloseAttribution(t *testing.T) {
	for _, test := range []struct {
		name, state, closedAt, eventAt string
		actor                          bool
	}{
		{"open", "OPEN", "", "", false},
		{"merged", "MERGED", "2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z", false},
		{"closed latest actor", "CLOSED", "2026-01-02T00:00:00Z", "2026-01-02T00:00:00Z", true},
		{"old close event", "CLOSED", "2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := New(Config{Exec: func(_ context.Context, bin string, args []string) ([]byte, error) {
				if bin != "gh" || !reflect.DeepEqual(args, []string{"api", "graphql", "-f", "query=" + githubQuery, "-f", "owner=example", "-f", "repo=project", "-F", "number=9"}) {
					t.Fatalf("unexpected command: %s %v", bin, args)
				}
				pr := map[string]any{"number": 9, "state": test.state, "url": "https://example.invalid/review/9", "headRefOid": "head", "baseRefOid": "base", "mergeCommit": map[string]any{"oid": "merge"}, "mergedBy": map[string]any{"id": "actor-id", "login": "merger"}, "closedAt": test.closedAt, "timelineItems": map[string]any{"nodes": []any{map[string]any{"createdAt": test.eventAt, "actor": map[string]any{"id": "closer-id", "login": "closer"}}}}}
				return json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
			}})
			got, err := cli.Observe(t.Context(), Ref{Provider: GitHub, Repository: "example/project", Number: 9})
			if err != nil {
				t.Fatal(err)
			}
			if got.State != strings.ToLower(test.state) || got.HeadSHA != "head" || got.BaseSHA != "base" || got.MergeSHA != "merge" || got.MergedBy.Login != "merger" {
				t.Fatalf("lost facts: %+v", got)
			}
			if (got.ClosedBy != nil) != test.actor {
				t.Fatalf("close attribution: %+v", got)
			}
		})
	}
}

func TestCodebaseDetailsOptionalIdentityAndCommitVersions(t *testing.T) {
	for _, state := range []string{Open, Merged, Closed} {
		t.Run(state, func(t *testing.T) {
			cli := New(Config{Exec: func(_ context.Context, bin string, args []string) ([]byte, error) {
				if bin != "bytedcli" || !reflect.DeepEqual(args, []string{"--json", "codebase", "mr", "get", "7", "-R", "example/project"}) {
					t.Fatalf("unexpected command %s %v", bin, args)
				}
				return json.Marshal(map[string]any{"status": "success", "data": map[string]any{"merge_request": map[string]any{"Number": 7, "Status": state, "ClosedBy": nil, "ClosedAt": nil, "MergedBy": map[string]any{"Id": "id", "Username": "merger", "Email": "never-export@example.invalid"}, "MergeCommitId": "merge", "SourceBranch": map[string]any{"Commit": map[string]any{"Id": "branch-advanced-after-review"}}, "Versions": []any{map[string]any{"Number": 3, "SourceCommitId": "latest"}, map[string]any{"Number": 1, "SourceCommitId": "old"}}}}})
			}})
			got, err := cli.Observe(t.Context(), Ref{Provider: Codebase, Repository: "example/project", Number: 7})
			if err != nil {
				t.Fatal(err)
			}
			if got.State != state || got.ClosedBy != nil || got.ClosedAt != "" || got.HeadSHA != "latest" || got.MergedBy.Login != "merger" {
				t.Fatalf("facts: %+v", got)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "never-export") {
				t.Fatal("account details leaked")
			}
		})
	}
}

func TestCodebaseCloserAndBranchHead(t *testing.T) {
	cli := New(Config{Exec: func(context.Context, string, []string) ([]byte, error) {
		return []byte(`{"status":"success","data":{"merge_request":{"Number":7,"Status":"closed","ClosedBy":{"Id":"actor","Username":"closer"},"ClosedAt":"2026-01-01T00:00:00Z","SourceBranch":{"Commit":{"Id":"head"}}}}}`), nil
	}})
	got, err := cli.Observe(t.Context(), Ref{Provider: Codebase, Repository: "example/project", Number: 7})
	if err != nil || got.ClosedBy == nil || got.ClosedBy.ID != "actor" || got.HeadSHA != "head" {
		t.Fatalf("facts=%+v error=%v", got, err)
	}
}

func TestFailedObservationDoesNotInventStateOrExposeProviderOutput(t *testing.T) {
	for _, test := range []struct {
		name, provider, body, kind string
		exit                       bool
	}{
		{"malformed", GitHub, `secret invalid json`, "invalid_response", false},
		{"cli failed", GitHub, `secret invalid json`, "query_failed", true},
		{"auth", GitHub, `{"errors":[{"type":"FORBIDDEN","message":"secret"}]}`, "authentication_required", true},
		{"missing", GitHub, `{"data":{"repository":{"pullRequest":null}}}`, "not_found", false},
		{"unknown github state", GitHub, `{"data":{"repository":{"pullRequest":{"number":7,"state":"FUTURE"}}}}`, "unknown_state", false},
		{"unknown codebase state", Codebase, `{"status":"success","data":{"merge_request":{"Number":7,"Status":"future"}}}`, "unknown_state", false},
		{"codebase failure", Codebase, `{"status":"error","error":{"code":"CODEBASE_NOT_FOUND","message":"secret"}}`, "not_found", true},
		{"wrong identity", Codebase, `{"status":"success","data":{"merge_request":{"Number":8,"Status":"closed"}}}`, "invalid_response", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := New(Config{Exec: func(context.Context, string, []string) ([]byte, error) {
				var err error
				if test.exit {
					err = errors.New("secret stderr")
				}
				return []byte(test.body), err
			}})
			got, err := cli.Observe(t.Context(), Ref{Provider: test.provider, Repository: "example/project", Number: 7})
			var failure *Error
			if !errors.As(err, &failure) || failure.Kind != test.kind || got != (Observation{}) {
				t.Fatalf("got=%+v error=%v", got, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("raw output leaked")
			}
		})
	}
}

func TestObservationCancellationAndInvalidReference(t *testing.T) {
	calls := 0
	cli := New(Config{Timeout: time.Millisecond, Exec: func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	_, err := cli.Observe(t.Context(), Ref{Provider: GitHub, Repository: "example/project", Number: 7})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
	for _, ref := range []Ref{{Provider: GitHub, Repository: "-Rbad", Number: 1}, {Provider: GitHub, Repository: "example/project"}, {Provider: GitHub, Repository: "invalid", Number: 1}, {Provider: "unknown", Repository: "example/project", Number: 1}} {
		if _, err := cli.Observe(t.Context(), ref); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
	if calls != 1 {
		t.Fatalf("invalid reference executed CLI: %d", calls)
	}
}

// This is GitHub's merged PR shape: baseRefOid remains pre-merge while
// baseRef.target.oid follows the live branch. They are different facts.
func TestGitHubTargetTipAndIntegratedLineage(t *testing.T) {
	const base = "6683ad911185022de67e0ad6c070736afd1f1feb"
	const merge = "faa0888fce6975e19cba7fa853faec413de66181"
	for _, tc := range []struct {
		name, state, tip, status, ancestor string
		integrated                         bool
	}{
		{"merged", "MERGED", merge, "identical", merge, true},
		{"advanced", "MERGED", strings.Repeat("e", 40), "ahead", merge, true},
		{"rewritten", "MERGED", base, "behind", base, false},
		{"diverged", "MERGED", strings.Repeat("e", 40), "diverged", base, false},
		{"open target drift", "OPEN", strings.Repeat("e", 40), "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := New(Config{Exec: func(_ context.Context, _ string, args []string) ([]byte, error) {
				calls++
				if calls == 1 {
					return json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"number": 203, "state": tc.state, "headRefOid": "d9de43492c08a956525fbc9758c65af8eba82f09", "baseRefOid": base, "baseRef": map[string]any{"target": map[string]any{"oid": tc.tip}}, "mergeCommit": map[string]any{"oid": merge}}}}})
				}
				if !reflect.DeepEqual(args, []string{"api", "repos/example/project/compare/" + merge + "..." + tc.tip}) {
					t.Fatal(args)
				}
				return json.Marshal(map[string]any{"status": tc.status, "merge_base_commit": map[string]any{"sha": tc.ancestor}})
			}})
			got, err := client.Observe(t.Context(), Ref{Provider: GitHub, Repository: "example/project", Number: 203})
			if err != nil || got.BaseSHA != base || got.TargetSHA != tc.tip || got.TargetContainsMerge != tc.integrated {
				t.Fatal(got, err)
			}
			if (calls == 2) != (tc.state == "MERGED") {
				t.Fatal(calls)
			}
		})
	}
}
