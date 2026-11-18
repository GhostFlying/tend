package maintenance

import (
	"cmp"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

const auditPageSize = 32
const ciCaseCode = "checks_owner_notice_missing"

func (c *Controller) recordChanges(changes []protocol.MaintenanceChange) {
	if c.state.Changes == nil {
		c.state.Changes = make(map[string]protocol.MaintenanceChange)
	}
	seen := make(map[string]bool, len(changes))
	for _, v := range changes {
		seen[v.IssueID] = true
		old, ok := c.state.Changes[v.IssueID]
		if !ok && v.State != "active" {
			continue
		}
		v.Sequence = old.Sequence
		if ok && old == v {
			if v.State != "active" && old.Sequence <= c.state.AuditedThrough {
				delete(c.state.Changes, v.IssueID)
			}
			continue
		}
		c.recordChange(v)
	}
	// The default Issue list omits closed/cancelled objects. Disappearance from a
	// complete successful snapshot proves only that it left this probe's scope.
	for id, old := range c.state.Changes {
		if seen[id] {
			continue
		}
		if old.State == "out_of_scope" {
			if old.Sequence <= c.state.AuditedThrough {
				delete(c.state.Changes, id)
			}
			continue
		}
		c.recordChange(protocol.MaintenanceChange{IssueID: id, Sequence: old.Sequence, State: "out_of_scope"})
	}
}

// A pending Issue keeps its place as observations coalesce. A change already
// covered by a completed or frozen page belongs to a later page.
func (c *Controller) recordChange(v protocol.MaintenanceChange) {
	through := c.state.AuditedThrough
	if active := c.state.Active; active != nil && active.Audit != nil {
		through = max(through, active.Audit.Through)
	}
	if v.Sequence <= through {
		c.state.ChangeSequence++
		v.Sequence = c.state.ChangeSequence
	}
	c.state.Changes[v.IssueID] = v
}

func (c *Controller) auditChanges() *protocol.MaintenanceAudit {
	a := &protocol.MaintenanceAudit{Since: c.state.AuditedThrough, Through: c.state.AuditedThrough, SnapshotAt: c.state.LastSnapshotAt, ObservationError: c.state.LastError, CIError: c.state.CIError}
	for _, v := range c.state.Changes {
		if v.Sequence > a.Through {
			a.Changes = append(a.Changes, v)
		}
	}
	slices.SortFunc(a.Changes, func(x, y protocol.MaintenanceChange) int { return cmp.Compare(x.Sequence, y.Sequence) })
	if len(a.Changes) > auditPageSize {
		a.More = true
		a.Changes = a.Changes[:auditPageSize]
	}
	if n := len(a.Changes); n > 0 {
		a.Through = a.Changes[n-1].Sequence
	}
	var pending []string
	for _, v := range c.state.Cases {
		if v.Status == "awaiting_verification" {
			pending = append(pending, v.ID)
		}
	}
	slices.Sort(pending)
	start, ok := slices.BinarySearch(pending, c.state.RecheckedAfter)
	if ok {
		start++
	}
	for i := 0; i < min(len(pending), auditPageSize); i++ {
		a.Recheck = append(a.Recheck, pending[(start+i)%len(pending)])
	}
	a.More = a.More || len(pending) > auditPageSize
	return a
}

func subjectID(code string, s protocol.MaintenanceSubject) string {
	return "case-" + digest([]string{code, s.IssueID, s.TaskID, s.RunID, s.WaitID})
}

func (c *Controller) report(req protocol.MaintenanceRequest) error {
	s := req.Subject
	if s == nil || req.CaseID == "" || req.CaseID == ciCaseCode || len(req.CaseID) > 100 || strings.TrimSpace(s.IssueID) == "" || strings.TrimSpace(s.TaskID) == "" || strings.TrimSpace(s.RunID) == "" || strings.TrimSpace(s.Evidence) == "" || strings.TrimSpace(s.Expected) == "" || strings.TrimSpace(req.Reason) == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "report requires a diagnostic code, original Issue/Task/Run, evidence, expected recovery and diagnosis")
	}
	id := subjectID(req.CaseID, *s)
	now := c.opts.Now().Unix()
	for i := range c.state.Cases {
		v := &c.state.Cases[i]
		if v.ID != id {
			continue
		}
		if v.Subject.Expected != s.Expected {
			return protocol.Errorf(protocol.CodeConflictPayload, "original case has a different recovery expectation")
		}
		v.LastSeen = now
		v.Evidence = s.Evidence
		return nil
	}
	original := *s
	c.state.Cases = append(c.state.Cases, protocol.MaintenanceCase{ID: id, Code: req.CaseID, Source: s.IssueID, Subject: &original, Observed: true, Fingerprint: id, Reason: req.Reason, Evidence: s.Evidence, Status: "open", FirstSeen: now, LastSeen: now})
	// If the reporting turn handles the case itself, its disposition consumes this notice.
	c.enqueue("case " + id)
	return nil
}

func ciSubject(f protocol.CIContinuation) protocol.MaintenanceSubject {
	return protocol.MaintenanceSubject{IssueID: f.IssueID, TaskID: f.TaskID, RunID: f.RunID, WaitID: f.WaitID, Evidence: "tend task ci-continuation " + f.TaskID + " " + f.RunID, Expected: "The original owner read this CI source and completed a turn with a formal disposition for it."}
}

func (c *Controller) observeCI(f protocol.CIContinuation, now int64) {
	if f.RunID == "" {
		return
	}
	s := ciSubject(f)
	id := subjectID(ciCaseCode, s)
	for i := range c.state.Cases {
		v := &c.state.Cases[i]
		if v.ID != id {
			continue
		}
		if f.NoticeRestored && v.Intervention == "" {
			v.Intervention = ciRestorationReceipt(s)
			if v.Status != "deferred" && v.Status != "resolved" {
				v.Status = "awaiting_verification"
			}
			c.consumeCaseNotice(id)
		}
		if v.Status == "deferred" && v.Intervention != "" && v.DeferUntil <= now {
			v.Status = "awaiting_verification"
		}
		if f.State == "processed" && f.OwnerTurn != "" {
			v.Verification = "Original owner turn " + f.OwnerTurn + " processed event " + f.SourceEvent + " for " + f.Head + " (" + f.Result + ")"
			v.Status = "resolved"
			c.consumeCaseNotice(id)
			v.Observed = false
		} else if f.State == "missing" && (v.Status == "not_observed" || (v.Status == "deferred" && v.DeferUntil <= now)) {
			v.Status = "open"
			v.Observed = true
			c.enqueue("case " + v.ID)
		} else {
			if f.State != "missing" && slices.Contains(c.state.Pending, "case "+id) {
				v.Status = "not_observed"
				c.consumeCaseNotice(id)
			}
			v.Observed = f.State == "missing"
		}
		v.LastSeen = now
		return
	}
	if f.State == "missing" {
		c.upsert(protocol.MaintenanceCase{ID: id, Code: ciCaseCode, Source: s.IssueID, Subject: &s, Evidence: s.Evidence, Fingerprint: id}, now)
	}
}

func (c *Controller) consumeCaseNotice(id string) {
	c.state.Pending = slices.DeleteFunc(c.state.Pending, func(reason string) bool { return reason == "case "+id })
}

func ciRestorationReceipt(s protocol.MaintenanceSubject) string {
	return "Original wait " + s.WaitID + " records its restored notification; read " + s.Evidence
}
