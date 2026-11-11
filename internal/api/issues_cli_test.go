package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
)

type issueTransport func(*http.Request) (*http.Response, error)

func (f issueTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIssueCLIRealHTTPRecoversBusinessIdentityAfterLostResponses(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]any{"config.json": map[string]string{"server": e.url}, "credentials.json": map[string]string{"client_id": "issue-cli-test-client", "owner_token": token, "deployment_id": deployment}} {
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	lost := 1
	var ids []string
	var envelopes [][]byte
	var hashes []string
	reads := 0
	httpClient := &http.Client{Transport: issueTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == "GET" {
			reads++
		}
		if request.Method == "POST" && request.URL.Path == "/api/v1/ops" {
			original, err := request.GetBody()
			if err != nil {
				return nil, err
			}
			raw, err := io.ReadAll(original)
			original.Close()
			if err != nil {
				return nil, err
			}
			var env protocol.Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				return nil, err
			}
			hash, err := protocol.PayloadHash(&env)
			if err != nil {
				return nil, err
			}
			ids = append(ids, env.RequestID)
			envelopes = append(envelopes, raw)
			hashes = append(hashes, hash)
			if !strings.Contains(stderr.String(), env.RequestID) {
				t.Error("request identity was not visible before HTTP")
			}
		}
		response, err := http.DefaultTransport.RoundTrip(request)
		if err == nil && request.Method == "POST" && request.URL.Path == "/api/v1/ops" && lost > 0 {
			lost--
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			return nil, errors.New("simulated lost response after server commit")
		}
		return response, err
	})}
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, ConfigDir: dir, HTTP: httpClient, IDs: e.ids}
	payload := `{"plan":{"title":"Owner CLI intent","project":"default","intent":"Investigate the vague goal","stages":[]}}`
	if code := cli.Main(t.Context(), env, []string{"issue", "create", "--data", payload}); code != 1 {
		t.Fatalf("lost responses exit=%d out=%s err=%s", code, &stdout, &stderr)
	}
	if len(ids) != 1 || e.count(`SELECT count(*) FROM issue_controls`) != 1 {
		t.Fatalf("unknown submission was repeated or duplicated issue: ids=%v", ids)
	}
	originalID := ids[0]
	var original protocol.Envelope
	if err := json.Unmarshal(envelopes[0], &original); err != nil || !protocol.ValidID(protocol.PrefixWorkItem, original.Target.ID) {
		t.Fatal("creation identity not fixed before sending", original, err)
	}
	if !strings.Contains(stderr.String(), "unknown_outcome") || !strings.Contains(stderr.String(), "issue show "+original.Target.ID) {
		t.Fatal("lost response omitted original-request recovery guidance", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	beforeReads := reads
	if code := cli.Main(t.Context(), env, []string{"issue", "show", original.Target.ID, "--json"}); code != 0 {
		t.Fatalf("reconciliation exit=%d out=%s err=%s", code, &stdout, &stderr)
	}
	var recorded protocol.IssueView
	if err := json.Unmarshal(stdout.Bytes(), &recorded); err != nil || recorded.IssueID != original.Target.ID || reads != beforeReads+1 || len(ids) != 1 {
		t.Fatalf("original committed request was not reconciled read-only: %+v err=%v reads=%d ids=%v", recorded, err, reads, ids)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Main(t.Context(), env, []string{"operation", "submit", "--data", string(envelopes[0])}); code != 0 {
		t.Fatalf("recovery exit=%d out=%s err=%s", code, &stdout, &stderr)
	}
	var response protocol.APIResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil || response.Operation == nil {
		t.Fatal(err, stdout.String())
	}
	var issue protocol.Issue
	_ = json.Unmarshal(response.Operation.Result, &issue)
	if response.Operation.RequestID != originalID || issue.IssueID != original.Target.ID || len(ids) != 2 || ids[1] != originalID || !bytes.Equal(envelopes[0], envelopes[1]) || hashes[0] != hashes[1] || e.count(`SELECT count(*) FROM issue_controls`) != 1 {
		t.Fatal("explicit saved-input submission changed its business identity or duplicated the Issue")
	}
	// The actual object key, rather than a generic request-result ledger,
	// makes another trace of the same immutable creation harmless.
	anotherTrace := original
	anotherTrace.RequestID = e.ids.New(protocol.PrefixRequest)
	sameObject, _ := json.Marshal(anotherTrace)
	stdout.Reset()
	stderr.Reset()
	if code := cli.Main(t.Context(), env, []string{"operation", "submit", "--data", string(sameObject)}); code != 0 {
		t.Fatal("same business key with another trace failed", code, stderr.String())
	}
	var current protocol.APIResponse
	if err := json.Unmarshal(stdout.Bytes(), &current); err != nil || current.Operation == nil || current.Operation.RequestID != anotherTrace.RequestID || e.count(`SELECT count(*) FROM issue_controls`) != 1 {
		t.Fatal("business identity depended on request cache", current, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := cli.Main(t.Context(), env, []string{"issue", "create", "--request-id", originalID, "--id", original.Target.ID, "--data", strings.Replace(payload, "Owner CLI intent", "Different intent", 1)}); code != 1 {
		t.Fatal("changed payload reused the original identity")
	}
	if e.count(`SELECT count(*) FROM issue_controls`) != 1 {
		t.Fatal("changed-payload recovery created another issue")
	}
	stdout.Reset()
	stderr.Reset()
	beforeReads = reads
	if code := cli.Main(t.Context(), env, []string{"issue", "pause", issue.IssueID}); code != 0 {
		t.Fatalf("first pause exit=%d %s", code, &stderr)
	}
	if reads != beforeReads+1 {
		t.Fatal("new control request did not read current issue once")
	}
	stdout.Reset()
	stderr.Reset()
	beforeReads = reads
	staleID := e.ids.New(protocol.PrefixRequest)
	if code := cli.Main(t.Context(), env, []string{"issue", "resume", issue.IssueID, "--request-id", staleID, "--revision", "1", "--epoch", "1"}); code != 1 {
		t.Fatal("stale request unexpectedly succeeded")
	}
	if reads != beforeReads || !strings.Contains(stdout.String(), "current_issue") || !strings.Contains(stdout.String(), "stale_revision") {
		t.Fatal("stale request was silently read/rebuilt", stdout.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("issue CLI created an extra request ledger", entries, err)
	}
}
