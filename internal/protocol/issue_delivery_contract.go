package protocol

import "strings"

// Delivery action fields describe caller intent only. Authority, source, current
// versions, actual execution and retained-result checks remain in App.
type deliveryActionFields struct {
	action             string
	required, optional []string
}

func deliveryActions() []deliveryActionFields {
	return []deliveryActionFields{
		{"submit_result", []string{"result"}, []string{"run_id", "independent_acceptance"}},
		{"prepare", []string{"task_id"}, []string{"candidate", "expected_task_control_revision", "acceptance_id", "reviewer_artifact_id"}},
		{"resume", []string{"task_id"}, []string{"candidate", "expected_task_control_revision", "acceptance_id", "reviewer_artifact_id"}},
		{"bind", []string{"task_id", "execution_task_id", "expected_task_control_revision"}, []string{"acceptance_id", "reviewer_artifact_id"}},
		{"report", []string{"task_id"}, []string{"acceptance_id", "reviewer_artifact_id"}},
		{"end", []string{"task_id"}, []string{"acceptance_id", "reviewer_artifact_id"}},
		{"complete", []string{"task_id"}, []string{"execution_task_id", "execution_run_id", "execution_duty_revision", "upgrade_id", "acceptance_id", "reviewer_artifact_id"}},
	}
}

// ValidateInput is shared by App and the native CLI; generated schemas derive
// their action alternatives from exactly the same field groups below.
func (p UpdateIssueDeliveryPayload) ValidateInput() error {
	present := map[string]bool{"task_id": p.TaskID != "", "result": p.Result != nil, "run_id": p.RunID != "", "independent_acceptance": p.IndependentAcceptance != nil, "candidate": p.Candidate != nil, "expected_task_control_revision": p.ExpectedTaskControlRevision != nil, "execution_task_id": p.ExecutionTaskID != "", "execution_run_id": p.ExecutionRunID != "", "execution_duty_revision": p.ExecutionDutyRevision != 0, "upgrade_id": p.UpgradeID != "", "acceptance_id": p.AcceptanceID != "", "reviewer_artifact_id": p.ReviewerArtifactID != ""}
	for _, rule := range deliveryActions() {
		if rule.action != p.Action {
			continue
		}
		for _, field := range rule.required {
			if !present[field] {
				return Errorf(CodeInvalidRequest, "%s requires %s", p.Action, field)
			}
		}
		// Existing payloads may retain fields from another action. Preserve
		// App's historical ignored-field semantics instead of adding gates.
		if strings.TrimSpace(p.Reason) == "" {
			return Errorf(CodeInvalidRequest, "delivery reason is required")
		}
		if p.ExecutionDutyRevision < 0 || p.ExecutionDutyRevision > 0 && (p.Action != "complete" || p.ExecutionRunID == "" || p.ExecutionTaskID == "") {
			return Errorf(CodeInvalidRequest, "execution_duty_revision requires complete with execution_task_id and execution_run_id")
		}
		if p.Action == "complete" && p.ExecutionRunID != "" && p.ExecutionTaskID == "" {
			return Errorf(CodeInvalidRequest, "execution_run_id requires execution_task_id")
		}
		return nil
	}
	return Errorf(CodeInvalidRequest, "action must be submit_result, prepare, resume, bind, report, end or complete")
}

func (UpdateIssueDeliveryPayload) JSONSchemaConstraints() map[string]any {
	branches := []any{}
	for _, rule := range deliveryActions() {
		props := map[string]any{"action": map[string]any{"const": rule.action, "description": "Uses " + strings.Join(append(append([]string{}, rule.required...), rule.optional...), ", ") + ". Existing common validation still applies; current duty state determines conditional candidate and Task requirements."}}
		branches = append(branches, map[string]any{"properties": props, "required": append([]string{"action"}, rule.required...)})
	}
	return map[string]any{"anyOf": branches, "allOf": []any{
		map[string]any{"properties": map[string]any{"reason": map[string]any{"minLength": 1}}, "required": []string{"reason"}},
		map[string]any{"if": map[string]any{"required": []string{"action", "execution_run_id"}, "properties": map[string]any{"action": map[string]any{"const": "complete"}}}, "then": map[string]any{"required": []string{"execution_task_id"}}},
		map[string]any{"properties": map[string]any{"execution_duty_revision": map[string]any{"minimum": 0}}}, map[string]any{"if": map[string]any{"required": []string{"execution_duty_revision"}, "properties": map[string]any{"execution_duty_revision": map[string]any{"minimum": 1}}}, "then": map[string]any{"required": []string{"execution_task_id", "execution_run_id"}, "properties": map[string]any{"action": map[string]any{"const": "complete"}}}},
	}}
}
