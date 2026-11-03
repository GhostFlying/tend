package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func ruleQuestionOpening(t *testing.T, h *harness, id string) (string, store.TaskRuleQuestionOrigin) {
	t.Helper()
	var raw string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM events WHERE object_type='decision' AND object_id=? AND kind='decision.opened' ORDER BY event_seq LIMIT 1`, id).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Origin store.TaskRuleQuestionOrigin `json:"task_rule_origin"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	return raw, body.Origin
}

func reviseRuleQuestionSpec(t *testing.T, h *harness, task string) {
	t.Helper()
	spec := h.storedSpec(task, h.task(task).SpecVersion)
	spec.Goal += " revised scope"
	spec.MaxImplementAttempts = 1
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &spec}, rev(h.task(task).ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestTaskQuestionOriginSpecReplacementKeepsCurrentBudget(t *testing.T) {
	h := newHarness(t)
	task, old := h.failImplementation()
	raw, origin := ruleQuestionOpening(t, h, old.DecisionID)
	if origin.Format != 1 || origin.TaskID != task || origin.SpecVersion != 1 || origin.ActionID == "" {
		t.Fatal("rule source not frozen", origin)
	}
	reviseRuleQuestionSpec(t, h, task)
	retired := readTaskDecision(t, h, old.DecisionID)
	if retired.Status != store.DecisionCancelled || retired.Version != old.Version+1 || retired.AnswerJSON != "" {
		t.Fatal("superseded question stayed actionable", retired)
	}
	impl, ok, _ := h.claim("linux-1")
	if !ok || impl.TaskID != task || impl.SpecVersion != 2 {
		t.Fatal(impl)
	}
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok || accept.Role != protocol.RoleAccept || accept.TaskID != task {
		t.Fatal(accept)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	budget := h.openDecision(task)
	if budget.DecisionID == old.DecisionID || h.task(task).BlockReason != protocol.BlockBudgetExhausted {
		t.Fatal("no new budget responsibility", budget)
	}
	_, newOrigin := ruleQuestionOpening(t, h, budget.DecisionID)
	if newOrigin.SpecVersion != 2 || newOrigin.SourceActionID != accept.ActionID || newOrigin.ActionID != "" || newOrigin.ImplementGeneration != h.task(task).ImplementGeneration {
		t.Fatal(newOrigin)
	}
	// The acceptance caused this budget question but does not own its new
	// implementation responsibility. A later control event must not retire it.
	for _, mode := range []protocol.ControlMode{protocol.ControlPaused, protocol.ControlManaged} {
		op := h.exec(owner, h.env("control.set_mode", taskTarget(task), protocol.SetControlModePayload{Mode: mode}, rev(h.task(task).ControlRevision)), SetControlMode{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if got := readTaskDecision(t, h, budget.DecisionID); !reflect.DeepEqual(got, budget) {
			t.Fatal("new budget attributed to completed acceptance", got)
		}
	}
	if got, _ := ruleQuestionOpening(t, h, old.DecisionID); got != raw {
		t.Fatal("original provenance rewritten")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{IncludeArchived: true})
		if err != nil {
			return err
		}
		states := map[string]string{}
		for _, a := range page.Items {
			states[a.DecisionID] = a.Status
		}
		if states[old.DecisionID] != "withdrawn" || states[budget.DecisionID] != "open" {
			t.Fatal("current budget blanket retired", states)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := h.count("conversation_messages")
	env := h.env("decisions.respond", protocol.Target{Type: "decision", ID: old.DecisionID}, protocol.AnswerDecisionPayload{Version: old.Version, Option: "retry the implementation"}, nil)
	denied := h.exec(owner, env, AnswerDecision{})
	if denied.Status != protocol.OpFailed || denied.Error.Code != protocol.CodeStaleRevision || h.count("conversation_messages") != before {
		t.Fatal(denied)
	}
	if got := h.exec(owner, env, AnswerDecision{}); !reflect.DeepEqual(got, denied) {
		t.Fatal("old failure replay changed", got)
	}
	if got := readTaskDecision(t, h, budget.DecisionID); !reflect.DeepEqual(got, budget) {
		t.Fatal("late old answer changed current question", got)
	}
}

func TestTaskQuestionOriginPauseAndLegacyAreNotReplacement(t *testing.T) {
	h := newHarness(t)
	task, current := h.failImplementation()
	legacy := h.ids.New(protocol.PrefixDecision)
	asked := h.task(task).ControlRevision
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertDecision(t.Context(), legacy, task, "Historical question without proven spec", `["wait"]`, `{}`, &asked, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	unscoped := h.exec(mainAgent, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{TaskID: task, Question: "Retain the product scope?", Options: []string{"yes", "no"}}, nil), OpenDecision{})
	if unscoped.Status != protocol.OpSucceeded {
		t.Fatal(unscoped)
	}
	var opened struct {
		ID string `json:"decision_id"`
	}
	if err := json.Unmarshal(unscoped.Result, &opened); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []protocol.ControlMode{protocol.ControlPaused, protocol.ControlManaged} {
		op := h.exec(owner, h.env("control.set_mode", taskTarget(task), protocol.SetControlModePayload{Mode: mode}, rev(h.task(task).ControlRevision)), SetControlMode{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if got := readTaskDecision(t, h, current.DecisionID); !reflect.DeepEqual(got, current) {
			t.Fatal("control revision invalidated current source", got)
		}
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{Status: "open"})
			if err != nil {
				return err
			}
			found := false
			for _, attention := range page.Items {
				if attention.DecisionID == current.DecisionID {
					found = true
					if attention.TaskContext == nil || !attention.TaskContext.QuestionCurrent {
						t.Fatal("pause falsely presented a current source as obsolete", attention)
					}
				}
			}
			if !found {
				t.Fatal("pause hid the current original question")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	reviseRuleQuestionSpec(t, h, task)
	for _, id := range []string{legacy, opened.ID} {
		if got := readTaskDecision(t, h, id); got.Status != store.DecisionOpen {
			t.Fatal("unknown or free-form provenance invented", got)
		}
	}
	if got := readTaskDecision(t, h, current.DecisionID); got.Status != store.DecisionCancelled {
		t.Fatal(got)
	}
}

func TestTaskQuestionOriginGuardRejectsBeforeAnswerSource(t *testing.T) {
	h := newHarness(t)
	task, d := h.failImplementation()
	// Represent a prior writer that advanced a real spec before this version's
	// convergence hook ran. The answer guard must independently reject it.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		body, err := store.GetSpec(t.Context(), tx.Q(), task, 1)
		if err != nil {
			return err
		}
		if err := tx.InsertSpec(t.Context(), task, 2, body, "", h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		row, err := store.GetTask(t.Context(), tx.Q(), task)
		if err != nil {
			return err
		}
		row.SpecVersion = 2
		row.ControlRevision++
		return tx.UpdateTask(t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, table := range []string{"conversation_messages", "plan_actions", "outbox"} {
		counts[table] = h.count(table)
	}
	env := h.env("decisions.respond", protocol.Target{Type: "decision", ID: d.DecisionID}, protocol.AnswerDecisionPayload{Version: d.Version, Option: "retry the implementation"}, nil)
	op := h.exec(owner, env, AnswerDecision{})
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(op)
	}
	for table, count := range counts {
		if h.count(table) != count {
			t.Fatal("stale answer created source or effect", table)
		}
	}
	if got := readTaskDecision(t, h, d.DecisionID); !reflect.DeepEqual(got, d) {
		t.Fatal("failed answer rewrote history", got)
	}
	if got := h.exec(owner, env, AnswerDecision{}); !reflect.DeepEqual(got, op) {
		t.Fatal("failure original identity changed", got)
	}
}

func TestTaskQuestionOriginGenerationAndAcceptedAnswerFacts(t *testing.T) {
	h := newHarness(t)
	task, d := h.failImplementation()
	request := h.env("actions.create", taskTarget(task), protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 2}, rev(h.task(task).ControlRevision))
	if op := h.exec(owner, request, CreateAction{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := readTaskDecision(t, h, d.DecisionID); got.Status != store.DecisionCancelled {
		t.Fatal("old action generation kept actionable", got)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.TaskID != task || next.Generation != 2 {
		t.Fatal(next)
	}
	h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(next, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "no_changes"}, Release{})
	current := h.openDecision(task)
	env := h.env("decisions.respond", protocol.Target{Type: "decision", ID: current.DecisionID}, protocol.AnswerDecisionPayload{Version: current.Version, Option: "revise the spec"}, nil)
	accepted := h.exec(owner, env, AnswerDecision{})
	if accepted.Status != protocol.OpSucceeded {
		t.Fatal(accepted)
	}
	answered := readTaskDecision(t, h, current.DecisionID)
	reviseRuleQuestionSpec(t, h, task)
	if got := readTaskDecision(t, h, current.DecisionID); !reflect.DeepEqual(got, answered) {
		t.Fatal("accepted answer changed", got)
	}
	if got := h.exec(owner, env, AnswerDecision{}); !reflect.DeepEqual(got, accepted) {
		t.Fatal("accepted original request changed", got)
	}
	semantic := h.env("decisions.respond", env.Target, protocol.AnswerDecisionPayload{Version: current.Version, Option: "revise the spec"}, nil)
	if got := h.exec(owner, semantic, AnswerDecision{}); got.Status != protocol.OpSucceeded {
		t.Fatal("historical accepted answer lost", got)
	}
}

func TestTaskQuestionOriginUnknownFormatsRemainUnclassified(t *testing.T) {
	h := newHarness(t)
	task, _ := h.failImplementation()
	asked := h.task(task).ControlRevision
	sources := []string{`{"format":99,"task_id":"TASK","spec_version":1}`, `{"format":1.0,"task_id":"TASK","spec_version":1}`, `{"format":1,"task_id":"other","spec_version":1}`, `{"format":1,"task_id":"TASK","spec_version":"1"}`}
	var ids []string
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, origin := range sources {
			id := h.ids.New(protocol.PrefixDecision)
			ids = append(ids, id)
			if err := tx.InsertDecision(t.Context(), id, task, "Unrecognized historical origin", `["wait"]`, `{}`, &asked, h.clock.Now().UnixMilli()); err != nil {
				return err
			}
			// Historical restore fixtures are not an authority to fabricate format-1
			// provenance. Recognized shape and an exact task binding are necessary.
			raw := strings.ReplaceAll(origin, "TASK", task)
			if _, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: h.ids.New(protocol.PrefixEvent), Source: "tend", ObjectType: "decision", ObjectID: id, ObjectVersion: 1, Kind: "decision.opened", BodyJSON: `{"task_rule_origin":` + raw + `}`, CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reviseRuleQuestionSpec(t, h, task)
	for _, id := range ids {
		if got := readTaskDecision(t, h, id); got.Status != store.DecisionOpen {
			t.Fatal("unrecognized provenance treated as certain", got)
		}
	}
}

func TestTaskQuestionOriginArtifactClearedToNULLRetiresAcceptanceQuestion(t *testing.T) {
	h := newHarness(t)
	task, acceptance := h.toAcceptanceClaim()
	if op := h.runAs(acceptance, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_failed"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old := h.openDecision(task)
	original, origin := ruleQuestionOpening(t, h, old.DecisionID)
	if origin.ArtifactID == "" {
		t.Fatal("acceptance question did not freeze candidate", origin)
	}
	if op := h.requestAction(task, protocol.ActionAccept, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	retry, ok, _ := h.claim("linux-1")
	if !ok || retry.Role != protocol.RoleAccept {
		t.Fatal(retry)
	}
	if op := h.runAs(retry, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task).CurrentArtifactID != "" {
		t.Fatal("candidate was not cleared")
	}
	var cleared bool
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT current_artifact_id IS NULL FROM tasks WHERE task_id=?`, task).Scan(&cleared); err != nil {
			return err
		}
		obsolete, err := store.TaskRuleQuestionObsolete(t.Context(), tx.Q(), old.DecisionID)
		if err != nil {
			return err
		}
		if !obsolete || !cleared {
			t.Fatal("SQL NULL did not invalidate old candidate", obsolete, cleared)
		}
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{IncludeArchived: true})
		if err != nil {
			return err
		}
		found := false
		for _, a := range page.Items {
			if a.DecisionID == old.DecisionID {
				found = true
				if a.Status != "withdrawn" || a.TaskContext != nil && a.TaskContext.QuestionCurrent {
					t.Fatal("old question remained current", a)
				}
			}
		}
		if !found {
			t.Fatal("history disappeared")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := readTaskDecision(t, h, old.DecisionID); got.Status != store.DecisionCancelled {
		t.Fatal(got)
	}
	before := h.count("conversation_messages")
	op := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: old.DecisionID}, protocol.AnswerDecisionPayload{Version: old.Version, Option: "retry acceptance"}, nil), AnswerDecision{})
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision || h.count("conversation_messages") != before {
		t.Fatal("late reply applied", op)
	}
	if got, _ := ruleQuestionOpening(t, h, old.DecisionID); got != original {
		t.Fatal("old provenance changed")
	}
}

func TestTaskQuestionOriginAcceptanceGenerationReplacementKeepsOnlyCurrentQuestion(t *testing.T) {
	h := newHarness(t)
	task, first := h.toAcceptanceClaim()
	if op := h.runAs(first, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_failed"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old := h.openDecision(task)
	original, origin := ruleQuestionOpening(t, h, old.DecisionID)
	if origin.ActionID != first.ActionID || origin.ArtifactID == "" {
		t.Fatal(origin)
	}
	if op := h.requestAction(task, protocol.ActionAccept, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	second, ok, _ := h.claim("linux-1")
	if !ok || second.Role != protocol.RoleAccept || second.Generation != 2 {
		t.Fatal(second)
	}
	if op := h.runAs(second, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_failed"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := h.openDecision(task)
	if current.DecisionID == old.DecisionID || h.task(task).CurrentArtifactID != origin.ArtifactID {
		t.Fatal("fixture changed artifact instead of acceptance generation", current)
	}
	if got := readTaskDecision(t, h, old.DecisionID); got.Status != store.DecisionCancelled || got.AnswerJSON != "" {
		t.Fatal("replaced acceptance question remained open", got)
	}
	for _, mode := range []protocol.ControlMode{protocol.ControlPaused, protocol.ControlManaged} {
		if op := h.exec(owner, h.env("control.set_mode", taskTarget(task), protocol.SetControlModePayload{Mode: mode}, rev(h.task(task).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if got := readTaskDecision(t, h, current.DecisionID); !reflect.DeepEqual(got, current) {
			t.Fatal("pause invalidated current acceptance generation", got)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{IncludeArchived: true})
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, a := range page.Items {
			if a.DecisionID == old.DecisionID {
				seen["old"] = true
				if a.Status != "withdrawn" || a.TaskContext != nil && a.TaskContext.QuestionCurrent {
					t.Fatal("old generation is current", a)
				}
			}
			if a.DecisionID == current.DecisionID {
				seen["current"] = true
				if a.Status != "open" || a.TaskContext == nil || !a.TaskContext.QuestionCurrent {
					t.Fatal("new generation is not current", a)
				}
			}
		}
		if !seen["old"] || !seen["current"] {
			t.Fatal("question history missing", seen)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := h.count("conversation_messages")
	late := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: old.DecisionID}, protocol.AnswerDecisionPayload{Version: old.Version, Option: "retry acceptance"}, nil), AnswerDecision{})
	if late.Status != protocol.OpFailed || late.Error.Code != protocol.CodeStaleRevision || h.count("conversation_messages") != before {
		t.Fatal("late old generation reply changed facts", late)
	}
	if got, _ := ruleQuestionOpening(t, h, old.DecisionID); got != original {
		t.Fatal("frozen origin changed")
	}
}
