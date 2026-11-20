package maintenance

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Command atomically accepts a revision-checked request and its durable work.
// Native side effects are performed later by Step, never in the control handler.
func (c *Controller) Command(ctx context.Context, action string, req protocol.MaintenanceRequest) (protocol.MaintenanceReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fault != nil {
		return protocol.MaintenanceReceipt{}, c.fault
	}
	if req.RequestID == "" || len(req.RequestID) > 256 {
		return protocol.MaintenanceReceipt{}, protocol.Errorf(protocol.CodeMissingRequestID, "request_id is required")
	}
	hash := digest(struct {
		Action  string
		Request protocol.MaintenanceRequest
	}{action, req})
	var oldHash string
	var revision int64
	err := c.db.QueryRowContext(ctx, `SELECT hash,revision FROM receipts WHERE id=?`, req.RequestID).Scan(&oldHash, &revision)
	if err == nil {
		if oldHash != hash {
			return protocol.MaintenanceReceipt{}, protocol.Errorf(protocol.CodeConflictPayload, "request identity conflicts with original input")
		}
		return commandReceipt(action, req, revision), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.MaintenanceReceipt{}, err
	}
	err = nil
	if req.ExpectedRevision != c.state.Revision {
		return protocol.MaintenanceReceipt{}, protocol.Errorf(protocol.CodeStaleRevision, "revision conflict; read status before issuing a new command")
	}
	if c.frozen() {
		return protocol.MaintenanceReceipt{}, protocol.Errorf(protocol.CodeInvalidTransition, "component installation is in progress")
	}
	before := c.state
	// Copy slice backing arrays before mutation so a rejected command is inert.
	c.state.Cases = slices.Clone(c.state.Cases)
	c.state.Pending = slices.Clone(c.state.Pending)
	duplicateSchedule := false
	switch action {
	case "check":
		c.state.CheckRequested = true
	case "audit":
		if req.ScheduledAudit {
			hour := c.opts.Now().UTC().Format("2006-01-02T15")
			if c.state.ScheduledHour >= hour {
				duplicateSchedule = true
				break
			}
			c.state.ScheduledHour = hour
			c.state.PendingAudits = append(c.state.PendingAudits, req.RequestID)
		}
		c.state.Failure = nil
		c.enqueue(action)
		c.state.LastAudit = c.opts.Now().Unix()
	case "review":
		head, headErr := hex.DecodeString(req.Head)
		if req.Base == "" || len(head) != 20 || headErr != nil || req.Head != hex.EncodeToString(head) {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "review requires base and exact head commit")
			break
		}
		if c.state.Review != nil && (c.state.Review.Phase == "queued" || c.state.Review.Phase == "running") {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "an independent review is already pending")
			break
		}
		c.state.Review = &protocol.MaintenanceReview{RequestID: req.RequestID, Base: req.Base, Head: req.Head, Phase: "queued"}
		c.enqueue("independent review requested")
		c.state.Failure = nil
	case "pause":
		c.state.Paused = true
	case "resume":
		c.state.Paused = false
		c.state.Failure = nil
	case "config":
		if req.Backend == nil {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "backend is required")
			break
		}
		if err = ValidateBackend(*req.Backend); err != nil {
			break
		}
		if req.Mode != "when-idle" && req.Mode != "interrupt" {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "require when-idle or interrupt")
			break
		}
		backend := *req.Backend
		c.state.PendingBackend = &backend
		c.state.SwitchMode = req.Mode
		c.state.Failure = nil
	case "report", "intervene", "resolve", "defer":
		if action == "report" {
			err = c.report(req)
			break
		}
		if req.Reason == "" {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "case disposition requires a reason")
			break
		}
		i := slices.IndexFunc(c.state.Cases, func(v protocol.MaintenanceCase) bool { return v.ID == req.CaseID })
		if i < 0 {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "case not found")
			break
		}
		if action == "defer" && req.Until <= c.opts.Now().Unix() {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "defer deadline must be in the future")
			break
		}
		v := &c.state.Cases[i]
		if action == "intervene" {
			if c.state.Paused || v.Subject == nil || v.Status == "resolved" || v.Status == "deferred" {
				err = protocol.Errorf(protocol.CodeInvalidTransition, "case is not available for intervention")
				break
			}
			if v.Intervention != "" && v.Intervention != req.Reason {
				if v.Code == ciCaseCode && v.Intervention == ciRestorationReceipt(*v.Subject) {
					break
				}
				err = protocol.Errorf(protocol.CodeConflictPayload, "reconcile the original intervention")
				break
			}
			c.consumeCaseNotice(v.ID)
			v.Intervention = req.Reason
			v.Status = "awaiting_verification"
			break
		}
		if action == "resolve" && v.Subject != nil {
			if strings.TrimSpace(req.Verification) == "" {
				err = protocol.Errorf(protocol.CodeInvalidRequest, "provide original work verification")
				break
			}
			if v.Code == ciCaseCode && v.Verification == "" {
				err = protocol.Errorf(protocol.CodeInvalidTransition, "original CI source consumption is unverified")
				break
			}
			if v.Code != ciCaseCode {
				v.Verification = req.Verification
			}
		}
		c.consumeCaseNotice(v.ID)
		v.Reason = req.Reason
		v.Status = "resolved"
		if action == "defer" {
			v.Status = "deferred"
			v.DeferUntil = req.Until
		}
	case "reconcile":
		err = c.reconcileNative(req)
	case "upgrade-plan":
		c.state.Upgrade, err = c.planUpgrade(req.RequestID, req.ReleaseDir)
	case "upgrade-apply":
		if c.state.Upgrade == nil || c.state.Upgrade.ID != req.UpgradeID || c.state.Upgrade.Phase != "planned" && c.state.Upgrade.Phase != "rejected" {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "matching planned upgrade required")
			break
		}
		plan, e := c.planUpgrade(c.state.Upgrade.ID, c.state.Upgrade.ReleaseDir)
		if e != nil {
			err = e
			break
		}
		original := *c.state.Upgrade
		original.Phase = "planned"
		if digest(plan) != digest(original) {
			err = protocol.Errorf(protocol.CodeInvalidRequest, "planned component changed")
			break
		}
		plan.Phase = "requested"
		c.state.Upgrade = plan
	default:
		err = protocol.Errorf(protocol.CodeInvalidRequest, "unknown control action")
	}
	if err != nil {
		c.state = before
		return protocol.MaintenanceReceipt{}, err
	}
	if !duplicateSchedule {
		c.state.Revision++
	}
	raw, err := marshalState(c.state)
	if err != nil {
		c.state = before
		return protocol.MaintenanceReceipt{}, err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		c.state = before
		return protocol.MaintenanceReceipt{}, err
	}
	defer tx.Rollback()
	if !duplicateSchedule {
		_, err = tx.ExecContext(ctx, `UPDATE state SET body=? WHERE id=1`, raw)
	}
	if err == nil {
		body, encodeErr := json.Marshal(struct {
			Action  string
			Request protocol.MaintenanceRequest
		}{action, req})
		err = encodeErr
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO receipts(id,hash,revision,body) VALUES(?,?,?,?)`, req.RequestID, hash, c.state.Revision, body)
		}
	}
	if err == nil {
		err = c.writeHistory(ctx, tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		c.state = before
		c.fault = err
		return protocol.MaintenanceReceipt{}, err
	}
	c.state.History = nil
	return commandReceipt(action, req, c.state.Revision), nil
}

func (c *Controller) reconcileNative(req protocol.MaintenanceRequest) error {
	a := c.state.Active
	if req.CreationID != "" || req.ExitEvidence != "" {
		if req.CreationID == "" || strings.TrimSpace(req.SessionID) == "" ||
			strings.TrimSpace(req.ExitEvidence) == "" || strings.TrimSpace(req.Verification) == "" ||
			strings.TrimSpace(req.Reason) == "" || req.TurnID != "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "creation exit requires original creation and native session IDs, exit evidence, verification of no turn, and a correlation reason")
		}
		if a == nil || a.ID != req.CreationID || a.Phase != "creating" || a.SessionID != "" || a.InputID != "" || a.TurnID != "" ||
			a.ObservedSessionID != "" && a.ObservedSessionID != req.SessionID {
			return protocol.Errorf(protocol.CodeInvalidTransition, "matching unresolved creation with no submitted input required")
		}
		exited := *a
		exited.ObservedSessionID = req.SessionID
		exited.ReconciledBy = req.RequestID
		exited.Phase = "creation-exited"
		exited.EndedAt = c.opts.Now().Unix()
		c.state.History = append(c.state.History, exited)
		c.state.Active = nil
		c.state.NativeError = ""
		return nil
	}
	if a == nil {
		return nil
	}
	active := *a
	if a.SessionID != "" && req.TurnID != "" && req.Reason != "" {
		active.TurnID = req.TurnID
		c.state.Active = &active
		return nil
	}
	if a.SessionID != "" || req.SessionID == "" || req.Reason == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "adopting an unknown creation requires original session_id and reason")
	}
	if a.ObservedSessionID != "" && a.ObservedSessionID != req.SessionID {
		return protocol.Errorf(protocol.CodeInvalidTransition, "original native session does not match the received thread identity")
	}
	active.SessionID = req.SessionID
	active.Phase = "idle"
	active.ReconciledBy = req.RequestID
	active.SessionProfile = ""
	c.state.Active = &active
	c.state.Predecessor = nil
	c.state.NativeError = ""
	return nil
}

func commandReceipt(action string, req protocol.MaintenanceRequest, revision int64) protocol.MaintenanceReceipt {
	v := protocol.MaintenanceReceipt{RequestID: req.RequestID, Revision: revision}
	if action == "report" && req.Subject != nil {
		v.CaseID = subjectID(req.CaseID, *req.Subject)
	}
	return v
}

func (c *Controller) Receipt(ctx context.Context, id string) (protocol.MaintenanceReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := protocol.MaintenanceReceipt{RequestID: id}
	var raw []byte
	err := c.db.QueryRowContext(ctx, `SELECT revision,body FROM receipts WHERE id=?`, id).Scan(&v.Revision, &raw)
	if err == nil {
		var original struct {
			Action  string
			Request protocol.MaintenanceRequest
		}
		err = json.Unmarshal(raw, &original)
		v.CaseID = commandReceipt(original.Action, original.Request, v.Revision).CaseID
		v.Action = original.Action
		v.Request = &original.Request
	}
	return v, err
}

func (c *Controller) enqueue(reason string) {
	if !slices.Contains(c.state.Pending, reason) {
		c.state.Pending = append(c.state.Pending, reason)
	}
}

func (c *Controller) finish(interrupted bool) {
	a := c.state.Active
	if n, ok := c.opts.Native.(interface{ ForgetUnstarted(string) }); ok {
		n.ForgetUnstarted(a.SessionID)
	}
	a.EndedAt = c.opts.Now().Unix()
	a.Phase = "completed"
	if a.Outcome == "failed" || a.Outcome == "interrupted" {
		a.Phase = a.Outcome
	}
	if interrupted {
		a.Phase = "interrupted"
		c.state.Predecessor = &protocol.MaintenancePredecessor{Agent: a.Backend.Agent, SessionID: a.SessionID}
		c.enqueue("interrupted handoff")
	}
	a.Prompt = ""
	c.state.History = append(c.state.History, *a)
	c.state.Active = nil
}

var errCreationNeedsReconciliation = errors.New("maintenance: native creation requires reconciliation")

// Step performs at most one native mutation after persisting its intent.
// An uncertain creation or input is retained, never sent again after restart.
func (c *Controller) Step(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fault != nil {
		return c.fault
	}
	if c.frozen() {
		return c.advanceUpgrade(ctx)
	}
	if c.state.Failure != nil {
		return nil
	}
	kept := slices.DeleteFunc(slices.Clone(c.state.Pending), func(reason string) bool {
		id, ok := strings.CutPrefix(reason, "case ")
		if !ok {
			return false
		}
		return !slices.ContainsFunc(c.state.Cases, func(v protocol.MaintenanceCase) bool { return v.ID == id && v.Observed && v.Status == "open" })
	})
	if len(kept) != len(c.state.Pending) {
		c.state.Pending = kept
		return c.save(ctx)
	}
	if c.state.Active != nil {
		a := c.state.Active
		if a.SessionID == "" {
			return errCreationNeedsReconciliation
		}
		mode := ReadMetadata
		if a.InputID != "" {
			mode = ReadWhenIdle
			if a.TurnID == "" {
				mode = ReadFullHistory
			}
		}
		native, err := c.opts.Native.Read(ctx, a.Backend, a.SessionID, mode)
		if a.Phase == "idle" && a.InputID == "" && a.SessionProfile != "" && len(c.state.PendingAudits) != 0 &&
			(errors.Is(err, ErrThreadUnavailable) || err == nil && native.Unusable) {
			return c.retireAuditThread(ctx)
		}
		if err != nil {
			return err
		}
		terminal := a.InputID == ""
		interrupted := false
		outcome := ""
		matches := 0
		for _, turn := range native.Turns {
			if turn.ID == a.TurnID && a.TurnID != "" || turn.InputID == a.InputID && a.InputID != "" {
				matches++
				if matches > 1 {
					return errors.New("maintenance: ambiguous native input history")
				}
				a.TurnID = turn.ID
				terminal = turn.Status == "completed" || turn.Status == "failed" || turn.Status == "interrupted"
				interrupted = turn.Status == "interrupted" && a.InterruptRequested
				outcome = turn.Status
				if !terminal && a.Phase != "interrupting" {
					a.Phase = "running"
				}
			}
		}
		if c.state.PendingBackend != nil && c.state.SwitchMode == "interrupt" && !terminal {
			if a.TurnID == "" {
				return errors.New("maintenance: cannot interrupt an unidentified native turn")
			}
			if a.Phase == "interrupting" {
				return nil
			}
			a.Phase = "interrupting"
			a.InterruptRequested = true
			if err := c.save(ctx); err != nil {
				return err
			}
			err := c.opts.Native.Interrupt(ctx, a.Backend, a.SessionID, a.TurnID)
			if errors.Is(err, ErrNotSent) {
				a.Phase = "running"
				a.InterruptRequested = false
				if e := c.save(ctx); e != nil {
					return e
				}
			}
			return err
		}
		if !terminal || native.Busy {
			return nil
		}
		if a.InputID != "" {
			a.Outcome = outcome
			if c.state.Review != nil && c.state.Review.Phase == "running" && c.state.Review.RequestID == a.InputID {
				c.state.Review.Phase = "completed"
				c.state.Review.Outcome = outcome
				if interrupted {
					c.state.Review.Phase = "queued"
					c.enqueue("independent review requested")
				} else {
					c.reviewContinuation(outcome)
				}
			}
			if outcome == "failed" || outcome == "interrupted" && !interrupted {
				c.restoreInputs(a, a.InputReasons)
				c.clearAuditThread(a.SessionID)
				c.finish(false)
				if c.state.PendingBackend == nil {
					failure := c.state.History[len(c.state.History)-1]
					c.state.Failure = &failure
				}
				return c.save(ctx)
			}
		}
		if a.Audit != nil && outcome == "completed" {
			c.state.AuditedThrough = max(c.state.AuditedThrough, a.Audit.Through)
			c.state.LastAuditCompleted = c.opts.Now().Unix()
			c.state.CompletedAudit = a.Audit
			if n := len(a.Audit.Recheck); n > 0 {
				c.state.RecheckedAfter = a.Audit.Recheck[n-1]
			}
		} else if a.Audit != nil && interrupted {
			c.restoreInputs(a, []string{"audit"})
		}
		if a.ScheduledAuditID != "" && outcome == "completed" {
			if c.state.PendingBackend == nil && a.SessionProfile != "" && !native.Unusable {
				c.state.IdleAuditSession = &protocol.MaintenanceAuditSession{
					SessionID: a.SessionID, RolloutPath: a.RolloutPath,
					SandboxPolicy: a.SandboxPolicy, Profile: a.SessionProfile,
				}
			}
			c.finish(false)
			// Repair and inline review queued during an audit continue on the
			// same thread, under a separate work record.
			if c.state.PendingBackend == nil && (c.state.Review != nil && c.state.Review.Phase == "queued" ||
				slices.ContainsFunc(c.state.Pending, func(reason string) bool { return reason != "audit" })) {
				c.state.Active = &protocol.MaintenanceSession{
					ID: c.opts.ID(), Backend: a.Backend, Phase: "idle", StartedAt: c.opts.Now().Unix(),
					SessionID: a.SessionID, RolloutPath: a.RolloutPath,
					SandboxPolicy: a.SandboxPolicy, SessionProfile: a.SessionProfile,
				}
			}
			return c.save(ctx)
		}
		if c.state.PendingBackend != nil {
			c.finish(interrupted)
			return c.save(ctx)
		}
		if a.InputID != "" {
			a.Audit = nil
			a.InputID = ""
			a.TurnID = ""
			a.Prompt = ""
			a.Phase = "idle"
			if len(c.state.Pending) == 0 {
				c.finish(false)
			}
			return c.save(ctx)
		}
	}
	if c.state.Active == nil && c.state.PendingBackend != nil {
		c.state.Backend = *c.state.PendingBackend
		c.state.PendingBackend = nil
		c.state.SwitchMode = ""
		c.state.IdleAuditSession = nil
		return c.save(ctx)
	}
	reviewQueued := c.state.Review != nil && c.state.Review.Phase == "queued"
	if c.state.Paused || len(c.state.Pending) == 0 && !reviewQueued {
		return nil
	}
	if c.state.Active == nil {
		instructions, err := os.ReadFile(c.cfg.InstructionsFile)
		if err != nil {
			return err
		}
		profile := digest(struct {
			Backend                 protocol.MaintenanceBackend
			Workspace, Instructions string
		}{c.state.Backend, c.cfg.Workspace, string(instructions)})
		if !reviewQueued && len(c.state.PendingAudits) != 0 && c.state.IdleAuditSession != nil {
			wait, err := c.selectAuditThread(ctx, profile)
			if err != nil || wait {
				return err
			}
		}
		if c.state.Active == nil {
			return c.createThread(ctx, string(instructions), profile)
		}
	}
	a := c.state.Active
	if a.InputID != "" {
		return nil
	}
	if c.state.Review != nil && c.state.Review.Phase == "queued" {
		return c.startReview(ctx)
	}
	a.InputID = c.opts.ID()
	a.InputReasons = slices.Clone(c.state.Pending)
	a.Phase = "sending"
	a.Prompt = fmt.Sprintf("Maintain Tend within the configured instructions. Triggers: %s. Read current evidence with `tend maintenance status --socket %s`, `tend maintenance cases --socket %s` and the configured Tend owner CLI. Diagnose before changing business state; healthy waits need no intervention. Repair detector omissions and add fault/healthy regressions. Resolve or defer diagnosed cases through the CLI. This is one bounded inspection; finish when no actionable work remains. Do not create another maintenance session.", strings.Join(c.state.Pending, ", "), shellArg(c.cfg.ControlSocket), shellArg(c.cfg.ControlSocket))
	c.state.Pending = nil
	if slices.Contains(a.InputReasons, "audit") {
		if len(c.state.PendingAudits) > 0 {
			a.ScheduledAuditID = c.state.PendingAudits[0]
			c.state.PendingAudits = c.state.PendingAudits[1:]
			if len(c.state.PendingAudits) > 0 {
				c.enqueue("audit")
			}
		}
		a.Audit = c.auditChanges()
		raw, _ := json.Marshal(a.Audit)
		a.Prompt += "\nInspect this bounded list of changes since the last completed audit and recheck the listed interventions against their original business evidence: " + string(raw)
		if a.ScheduledAuditID != "" {
			a.Prompt += "\nIndependently look for missed maintenance work even if the list is empty. Choose which current evidence to inspect."
		}
	}
	if a.Predecessor != nil {
		a.Prompt += fmt.Sprintf(" Previous agent: %s; session ID: %s.", a.Predecessor.Agent, a.Predecessor.SessionID)
	}
	if err := c.save(ctx); err != nil {
		return err
	}
	id, err := c.opts.Native.Start(ctx, a.Backend, nativeThread(a), a.InputID, a.Prompt)
	if err != nil {
		if errors.Is(err, ErrNotSent) {
			c.restoreInputs(a, a.InputReasons)
			if a.ScheduledAuditID != "" && errors.Is(err, ErrThreadUnavailable) {
				if e := c.retireAuditThread(ctx); e != nil {
					return e
				}
				return err
			}
			a.ScheduledAuditID = ""
			a.Audit = nil
			a.InputID = ""
			a.Prompt = ""
			a.Phase = "idle"
			if e := c.save(ctx); e != nil {
				return e
			}
		}
		return err
	}
	a.TurnID = id
	a.Phase = "running"
	return c.save(ctx)
}

func (c *Controller) createThread(ctx context.Context, instructions, profile string) error {
	c.state.Active = &protocol.MaintenanceSession{ID: c.opts.ID(), Backend: c.state.Backend, Phase: "creating", StartedAt: c.opts.Now().Unix(), Predecessor: c.state.Predecessor, SessionProfile: profile}
	if err := c.save(ctx); err != nil {
		return err
	}
	thread, err := c.opts.Native.NewThread(ctx, c.state.Backend, c.cfg.Workspace, instructions)
	if err == nil && thread.ID == "" {
		err = errors.New("maintenance: native creation response omitted thread identity")
	}
	if err != nil {
		if errors.Is(err, ErrNotSent) {
			c.state.Active = nil
		} else {
			c.state.Active.CreationError = err.Error()
			c.state.Active.ObservedSessionID = thread.ID
			c.state.Active.RolloutPath = thread.Path
			c.state.Active.SandboxPolicy = thread.Sandbox
		}
		// Even when the caller's deadline caused the missing reply, keep
		// the first native diagnosis with the original write-ahead intent.
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if e := c.save(persistCtx); e != nil {
			return e
		}
		return err
	}
	c.state.Active.SessionID = thread.ID
	c.state.Active.RolloutPath = thread.Path
	c.state.Active.SandboxPolicy = thread.Sandbox
	c.state.Active.Phase = "idle"
	c.state.Predecessor = nil
	return c.save(ctx)
}

func (c *Controller) selectAuditThread(ctx context.Context, profile string) (wait bool, err error) {
	session := c.state.IdleAuditSession
	if session.Profile != profile {
		c.state.IdleAuditSession = nil
		return false, nil
	}
	native, err := c.opts.Native.Read(ctx, c.state.Backend, session.SessionID, ReadMetadata)
	if errors.Is(err, ErrThreadUnavailable) || err == nil && native.Unusable {
		c.state.IdleAuditSession = nil
		return false, nil
	}
	if err != nil || native.Busy {
		return true, err
	}
	c.state.Active = &protocol.MaintenanceSession{
		ID: c.opts.ID(), Backend: c.state.Backend, Phase: "idle", StartedAt: c.opts.Now().Unix(),
		SessionID: session.SessionID, RolloutPath: session.RolloutPath,
		SandboxPolicy: session.SandboxPolicy, SessionProfile: session.Profile,
	}
	c.state.IdleAuditSession = nil
	return false, nil
}

func (c *Controller) clearAuditThread(id string) {
	if c.state.IdleAuditSession != nil && c.state.IdleAuditSession.SessionID == id {
		c.state.IdleAuditSession = nil
	}
}

func (c *Controller) retireAuditThread(ctx context.Context) error {
	a := c.state.Active
	c.clearAuditThread(a.SessionID)
	if n, ok := c.opts.Native.(interface{ ForgetUnstarted(string) }); ok {
		n.ForgetUnstarted(a.SessionID)
	}
	a.EndedAt = c.opts.Now().Unix()
	a.Phase = "unavailable"
	a.Prompt = ""
	c.state.History = append(c.state.History, *a)
	c.state.Active = nil
	return c.save(ctx)
}

func (c *Controller) restoreInputs(a *protocol.MaintenanceSession, reasons []string) {
	if a.ScheduledAuditID != "" {
		c.state.PendingAudits = append([]string{a.ScheduledAuditID}, c.state.PendingAudits...)
	}
	for _, reason := range reasons {
		c.enqueue(reason)
	}
}

func shellArg(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
