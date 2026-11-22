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
		{"register_pr", []string{"pr_url"}, nil},
		{"submit_result", []string{"result"}, []string{"run_id", "independent_acceptance"}},
		{"prepare", []string{"task_id"}, []string{"candidate", "expected_task_control_revision", "acceptance_id", "reviewer_artifact_id"}},
		{"start_next", []string{"task_id", "candidate", "previous_execution", "expected_task_control_revision"}, []string{"run_id"}},
		{"resume", []string{"task_id"}, []string{"candidate", "expected_task_control_revision", "acceptance_id", "reviewer_artifact_id"}},
		{"recover_preparation", []string{"task_id", "expected_task_control_revision"}, nil},
		{"bind", []string{"task_id", "execution_task_id", "expected_task_control_revision"}, []string{"acceptance_id", "reviewer_artifact_id"}},
		{"report", []string{"task_id"}, []string{"acceptance_id", "reviewer_artifact_id", "run_id", "proposed_implementation_scope"}},
		{"end", []string{"task_id"}, []string{"acceptance_id", "reviewer_artifact_id"}},
		{"complete", []string{"task_id"}, []string{"execution_task_id", "execution_run_id", "execution_duty_revision", "upgrade_id", "acceptance_id", "reviewer_artifact_id"}},
	}
}

// ValidateInput is shared by App and the native CLI; generated schemas derive
// their action alternatives from exactly the same field groups below.
func (p UpdateIssueDeliveryPayload) ValidateInput() error {
	present := map[string]bool{"previous_execution": p.PreviousExecution != nil, "pr_url": p.PRURL != "", "task_id": p.TaskID != "", "result": p.Result != nil, "run_id": p.RunID != "", "independent_acceptance": p.IndependentAcceptance != nil, "candidate": p.Candidate != nil, "expected_task_control_revision": p.ExpectedTaskControlRevision != nil, "execution_task_id": p.ExecutionTaskID != "", "execution_run_id": p.ExecutionRunID != "", "execution_duty_revision": p.ExecutionDutyRevision != 0, "upgrade_id": p.UpgradeID != "", "acceptance_id": p.AcceptanceID != "", "reviewer_artifact_id": p.ReviewerArtifactID != ""}
	if p.PreviousExecution != nil && (p.Action != "start_next" || p.PreviousExecution.DutyRevision <= 0 || p.PreviousExecution.ExecutionRunID == "") {
		return Errorf(CodeInvalidRequest, "previous_execution requires start_next and its original execution_run_id and positive duty_revision")
	}
	if p.Action == "start_next" && (p.InstanceID != "" || p.AcceptanceID != "" || p.ReviewerArtifactID != "" || p.ExecutionTaskID != "" || p.ExecutionRunID != "" || p.UpgradeID != "") {
		return Errorf(CodeInvalidRequest, "start_next requires a new candidate and cannot inherit execution, acceptance or upgrade references")
	}
	if p.PRURL != "" && p.Action != "register_pr" {
		return Errorf(CodeInvalidRequest, "pr_url requires register_pr")
	}
	for _, rule := range deliveryActions() {
		if rule.action != p.Action {
			continue
		}
		for _, field := range rule.required {
			if !present[field] {
				return Errorf(CodeInvalidRequest, "%s requires %s", p.Action, field)
			}
		}
		if p.Action == "register_pr" {
			if _, err := ParseGitHubPullRequestURL(p.PRURL); err != nil {
				return err
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
		if p.ProposedImplementationScope != "" && (p.Action != "report" || strings.TrimSpace(p.ProposedImplementationScope) == "") {
			return Errorf(CodeInvalidRequest, "proposed_implementation_scope requires a nonempty scope on action report")
		}
		if p.Action == "complete" && p.ExecutionRunID != "" && p.ExecutionTaskID == "" {
			return Errorf(CodeInvalidRequest, "execution_run_id requires execution_task_id")
		}
		return nil
	}
	return Errorf(CodeInvalidRequest, "action must be register_pr, submit_result, prepare, start_next, resume, recover_preparation, bind, report, end or complete")
}

func (UpdateIssueDeliveryPayload) JSONSchemaConstraints() map[string]any {
	branches := []any{}
	for _, rule := range deliveryActions() {
		props := map[string]any{"action": map[string]any{"type": "string", "const": rule.action, "description": "Uses " + strings.Join(append(append([]string{}, rule.required...), rule.optional...), ", ") + ". Existing common validation still applies; current duty state determines conditional candidate and Task requirements."}}
		branches = append(branches, map[string]any{"properties": props, "required": append([]string{"action"}, rule.required...)})
	}
	return map[string]any{"anyOf": branches, "allOf": []any{
		map[string]any{
			"if":   map[string]any{"required": []string{"pr_url"}},
			"then": map[string]any{"properties": map[string]any{"action": map[string]any{"const": "register_pr"}}},
		},
		map[string]any{
			"if":   map[string]any{"required": []string{"previous_execution"}},
			"then": map[string]any{"properties": map[string]any{"action": map[string]any{"const": "start_next"}}},
		},
		map[string]any{"properties": map[string]any{"reason": map[string]any{"type": "string", "minLength": 1}}, "required": []string{"reason"}},
		map[string]any{"if": map[string]any{"required": []string{"action", "execution_run_id"}, "properties": map[string]any{"action": map[string]any{"type": "string", "const": "complete"}}}, "then": map[string]any{"required": []string{"execution_task_id"}}},
		map[string]any{"properties": map[string]any{"execution_duty_revision": map[string]any{"type": "integer", "minimum": 0}}}, map[string]any{"if": map[string]any{"required": []string{"execution_duty_revision"}, "properties": map[string]any{"execution_duty_revision": map[string]any{"type": "integer", "minimum": 1}}}, "then": map[string]any{"required": []string{"execution_task_id", "execution_run_id"}, "properties": map[string]any{"action": map[string]any{"type": "string", "const": "complete"}}}},
	}}
}
