package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/logging"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCanonicalIssueTransitionsUseDefaultInfo(t *testing.T) {
	h := newHarness(t)
	var out bytes.Buffer
	log, err := logging.NewText(&out, "info")
	if err != nil {
		t.Fatal(err)
	}
	WithLogger(log)(h.app)
	issue := createCanonicalOwnerTestIssue(t, h)
	if !strings.Contains(out.String(), "object_type=issue ") {
		t.Fatal("create omitted canonical Issue transition", out.String())
	}
	out.Reset()
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	if !strings.Contains(out.String(), "event=issue.status_changed") {
		t.Fatal("status omitted canonical Issue transition", out.String())
	}
	out.Reset()
	op := h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "Keep real diagnosis", NextStep: "Continue"}, rev(issue.Revision)), UpdateIssueProgress{})
	if op.Error != nil {
		t.Fatal(op)
	}
	if !strings.Contains(out.String(), "event=issue.progress_updated") {
		t.Fatal("progress omitted canonical Issue transition", out.String())
	}
	t.Log(out.String())
	if !strings.Contains(out.String(), "object_type=issue ") {
		t.Fatal("canonical issue transition absent at default info")
	}
}
