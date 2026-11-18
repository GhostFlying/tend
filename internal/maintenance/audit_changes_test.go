package maintenance

import (
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFault_MaintenanceAuditConsumesOnlyCompletedFrozenPage(t *testing.T) {
	c, n, cfg, opts := fixture(t)
	var changes []protocol.MaintenanceChange
	for i := range auditPageSize + 2 {
		changes = append(changes, protocol.MaintenanceChange{IssueID: fmt.Sprintf("issue-%d", i), State: "active", Revision: 1})
	}
	c.recordChanges(changes)
	launch(t, c)
	s := c.Status()
	if s.LastAudit == 0 || s.LastAuditCompleted != 0 || s.AuditedThrough != 0 || !s.Active.Audit.More || len(s.Active.Audit.Changes) != auditPageSize {
		t.Fatal(s)
	}
	changes[0].Revision = 2
	c.recordChanges(changes)
	command(t, c, "check", protocol.MaintenanceRequest{})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.lock = nil
	restarted, err := Open(t.Context(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	n.busy, n.complete, n.outcome = false, true, "failed"
	step(t, restarted)
	s = restarted.Status()
	if s.AuditedThrough != 0 || s.LastAuditCompleted != 0 || s.Failure == nil {
		t.Fatal("failed audit consumed changes", s)
	}
	command(t, restarted, "audit", protocol.MaintenanceRequest{})
	step(t, restarted)
	step(t, restarted)
	page := restarted.Status().Active.Audit
	if page == nil || !page.More {
		t.Fatal(page)
	}
	n.busy, n.complete, n.outcome = false, true, "completed"
	step(t, restarted)
	s = restarted.Status()
	if s.AuditedThrough != page.Through || s.LastAuditCompleted == 0 || s.CompletedAudit.Through != page.Through {
		t.Fatal(s)
	}
	command(t, restarted, "audit", protocol.MaintenanceRequest{})
	step(t, restarted)
	step(t, restarted)
	next := restarted.Status().Active.Audit
	if len(next.Changes) != 2 || next.More || next.Changes[1].IssueID != "issue-0" || next.Changes[1].Revision != 2 {
		t.Fatal(next)
	}
}

func TestMaintenanceAuditAdvancesWhenEveryPendingIssueChanges(t *testing.T) {
	c, n, _, _ := fixture(t)
	changes := make([]protocol.MaintenanceChange, auditPageSize+1)
	for i := range changes {
		changes[i] = protocol.MaintenanceChange{IssueID: fmt.Sprintf("issue-%02d", i), State: "active", Revision: 1}
	}
	c.recordChanges(changes)
	launch(t, c)
	first := c.Status().Active.Audit
	if !first.More || len(first.Changes) != auditPageSize {
		t.Fatal(first)
	}
	for i := range changes {
		changes[i].Revision++
	}
	c.recordChanges(changes)
	n.complete, n.busy, n.outcome = true, false, "completed"
	step(t, c)
	for i := range changes {
		changes[i].Revision++
	}
	c.recordChanges(changes)
	launch(t, c)
	next := c.Status().Active.Audit
	last := changes[auditPageSize]
	if !next.More || len(next.Changes) != auditPageSize || next.Changes[0].IssueID != last.IssueID || next.Changes[0].Revision != last.Revision {
		t.Fatalf("next audit did not lead with the latest revision of the waiting Issue: %+v", next)
	}
	if next.Changes[1].IssueID != changes[0].IssueID || next.Changes[1].Revision != changes[0].Revision {
		t.Fatalf("change after the first page was frozen was lost: %+v", next)
	}
}

func TestMaintenanceReportedCaseSurvivesRestartAndRechecksIntervention(t *testing.T) {
	c, _, cfg, opts := fixture(t)
	subject := protocol.MaintenanceSubject{IssueID: "issue", TaskID: "owner", RunID: "original", WaitID: "wait", Evidence: "original observation", Expected: "same owner handles the result"}
	report := protocol.MaintenanceRequest{CaseID: "same_chain", Reason: "verified outside the program probe", Subject: &subject}
	command(t, c, "report", report)
	subject.Evidence = "second reading"
	command(t, c, "report", report)
	s := c.Status()
	if len(s.Cases) != 1 || len(s.Pending) != 1 || s.Cases[0].Subject.Evidence != "original observation" || s.Cases[0].Evidence != "second reading" {
		t.Fatal(s)
	}
	id := s.Cases[0].ID
	command(t, c, "defer", protocol.MaintenanceRequest{CaseID: id, Reason: "owner paused this work", Until: 1010})
	if _, err := c.Command(t.Context(), "intervene", protocol.MaintenanceRequest{RequestID: "bad", ExpectedRevision: c.Status().Revision, CaseID: id, Reason: "skip deferral"}); err == nil {
		t.Fatal("deferred case accepted intervention")
	}
	subject.RunID = "another-original"
	command(t, c, "report", report)
	id = c.Status().Cases[1].ID
	command(t, c, "intervene", protocol.MaintenanceRequest{CaseID: id, Reason: "original action acknowledged"})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.lock = nil
	d, err := Open(t.Context(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	command(t, d, "report", report)
	if len(d.Status().Pending) != 0 || len(d.Status().Cases) != 2 {
		t.Fatal(d.Status())
	}
	launch(t, d)
	a := d.Status().Active.Audit
	if len(a.Recheck) != 1 || a.Recheck[0] != id {
		t.Fatal(a)
	}
	if _, err := d.Command(t.Context(), "resolve", protocol.MaintenanceRequest{RequestID: "guess", ExpectedRevision: d.Status().Revision, CaseID: id, Reason: "action sent"}); err == nil {
		t.Fatal("action was treated as recovery")
	}
	command(t, d, "resolve", protocol.MaintenanceRequest{CaseID: id, Reason: "original work verified", Verification: "owner processed original evidence"})
	if d.Status().Cases[1].Verification == "" {
		t.Fatal(d.Status())
	}
}

func TestMaintenanceHourlyTimerRetriesAcrossRestart(t *testing.T) {
	c, n, cfg, opts := fixture(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c.lock = nil
	for range 2 {
		if err := Audit(t.Context(), cfg, opts); err != nil {
			t.Fatal(err)
		}
		n.complete, n.busy = true, false
	}
	d, err := Open(t.Context(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if s := d.Status(); n.started != 1 || s.Active != nil || len(s.Pending) != 0 || s.LastAuditCompleted == 0 {
		t.Fatal("duplicate timer occurrence started maintenance twice or lost original completion", n.started, s)
	}
}
