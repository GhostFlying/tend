package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueSetDependenciesCLIRealHTTP(t *testing.T) {
	e := newEnv(t)
	e.configure()
	owner := e.login(protocol.ScopeOwner)
	target := createOwnerIssueHTTP(t, e, owner, "")
	first := createOwnerIssueHTTP(t, e, owner, "")
	second := createOwnerIssueHTTP(t, e, owner, "")
	c := newCapabilityCLI(t, e)

	var counts struct{ reads, writes int }
	var lastWrite []byte
	var loseResponse, unauthenticated bool
	c.env.HTTP = &http.Client{Transport: issueTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			counts.reads++
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/ops" {
			counts.writes++
			body, err := r.GetBody()
			if err != nil {
				return nil, err
			}
			lastWrite, err = io.ReadAll(body)
			body.Close()
			if err != nil {
				return nil, err
			}
			if unauthenticated {
				r = r.Clone(r.Context())
				r.Header.Del("Authorization")
			}
			if loseResponse {
				response, err := http.DefaultTransport.RoundTrip(r)
				if err != nil {
					return nil, err
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				return nil, errors.New("simulated lost response after server commit")
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})}

	read := func(want ...string) protocol.IssueView {
		t.Helper()
		prior := counts
		var view protocol.IssueView
		if err := json.Unmarshal(c.run("issue", "show", target.IssueID, "--json"), &view); err != nil {
			t.Fatal(err)
		}
		if view.IssueID != target.IssueID || !slices.Equal(view.DependsOn, want) {
			t.Fatalf("depends_on=%v, want %v", view.DependsOn, want)
		}
		if counts.reads != prior.reads+1 || counts.writes != prior.writes {
			t.Fatal("issue show did not use a single read")
		}
		return view
	}
	flags := func(revision, epoch int64, payload string) []string {
		return []string{target.IssueID, "--revision", strconv.FormatInt(revision, 10), "--epoch", strconv.FormatInt(epoch, 10),
			"--request-id", e.ids.New(protocol.PrefixRequest), "--data", payload}
	}
	firstPayload := fmt.Sprintf(`{"depends_on":[%q]}`, first.IssueID)
	secondPayload := fmt.Sprintf(`{"depends_on":[%q]}`, second.IssueID)

	initial := read()
	pinned := flags(initial.Revision, initial.AssignmentEpoch, firstPayload)
	prior := counts
	dry := append(pinned, "--dry-run")
	original := c.run(append([]string{"issue", "set-dependencies"}, dry...)...)
	alias := c.run(append([]string{"issue", "dependencies"}, dry...)...)
	if counts != prior || !bytes.Equal(original, alias) {
		t.Fatalf("pinned dry-runs sent HTTP or differed:\n%s\n%s", original, alias)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(original, &envelope); err != nil || envelope.Operation != "issues.dependencies" ||
		envelope.Target != (protocol.Target{Type: "issue", ID: target.IssueID}) ||
		envelope.ExpectedControlRevision == nil || *envelope.ExpectedControlRevision != initial.Revision ||
		envelope.RequestID != pinned[6] {
		t.Fatalf("invalid Envelope: %s (%v)", original, err)
	}
	prior = counts
	c.run("issue", "set-dependencies", target.IssueID, "--data", firstPayload, "--dry-run")
	if counts.reads != prior.reads+1 || counts.writes != prior.writes {
		t.Fatal("unpinned dry-run did not use only an Issue read")
	}
	read()
	c.run(append([]string{"issue", "dependencies"}, pinned...)...)
	if !bytes.Equal(bytes.TrimSpace(original), bytes.TrimSpace(lastWrite)) {
		t.Fatalf("legacy submission changed the original Envelope: %s", lastWrite)
	}
	current := read(first.IssueID)

	prior = counts
	for _, tc := range []struct {
		revision, epoch int64
		payload, error  string
	}{
		{current.Revision, current.AssignmentEpoch, "{}", "invalid_request"},
		{current.Revision, current.AssignmentEpoch, `{"depends_on":null}`, "invalid_request"},
		{initial.Revision, current.AssignmentEpoch, secondPayload, "stale_revision"},
		{current.Revision, current.AssignmentEpoch + 1, secondPayload, "stale_revision"},
	} {
		code, _ := c.call(append([]string{"issue", "set-dependencies"}, flags(tc.revision, tc.epoch, tc.payload)...)...)
		if code == 0 || !strings.Contains(c.stderr.String(), tc.error) {
			t.Fatalf("expected %s, got exit=%d stderr=%s", tc.error, code, &c.stderr)
		}
	}
	if counts.reads != prior.reads || counts.writes != prior.writes+4 {
		t.Fatal("a pinned rejection did not use one write and preserve original control values")
	}
	unauthenticated = true
	if code, _ := c.call(append([]string{"issue", "set-dependencies"}, flags(current.Revision, current.AssignmentEpoch, secondPayload)...)...); code == 0 {
		t.Fatal("unauthenticated mutation succeeded")
	}
	unauthenticated = false
	if current.Revision != read(first.IssueID).Revision {
		t.Fatal("rejected mutations changed the Issue")
	}

	pinned = flags(current.Revision, current.AssignmentEpoch, secondPayload)
	prior = counts
	loseResponse = true
	code, _ := c.call(append([]string{"issue", "set-dependencies"}, pinned...)...)
	loseResponse = false
	if code == 0 || !strings.Contains(c.stderr.String(), "unknown_outcome") ||
		!strings.Contains(c.stderr.String(), pinned[6]) ||
		!strings.Contains(c.stderr.String(), "issue show "+target.IssueID) ||
		counts.reads != prior.reads || counts.writes != prior.writes+1 {
		t.Fatalf("unknown outcome lost original identity or repeated submission: exit=%d %s", code, &c.stderr)
	}
	current = read(second.IssueID)
	c.run(append([]string{"issue", "set-dependencies"}, flags(current.Revision, current.AssignmentEpoch, `{"depends_on":[]}`)...)...)
	read()
}
