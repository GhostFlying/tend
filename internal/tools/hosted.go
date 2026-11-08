package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type hostedMutationArgs struct {
	Reason                  string          `json:"reason,omitempty"`
	TargetID                string          `json:"target_id,omitempty"`
	ExpectedControlRevision *int64          `json:"expected_control_revision,omitempty"`
	EffectSeq               int64           `json:"effect_seq"`
	Payload                 json.RawMessage `json:"payload"`
}
type hostedPlanningReadArgs struct {
	IssueID string `json:"issue_id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Status  string `json:"status,omitempty"`
	After   string `json:"after,omitempty"`
}

type hostedCategoryListArgs struct {
	State string `json:"state,omitempty"`
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
type hostedIssueListArgs struct {
	Project       string              `json:"project,omitempty"`
	ParentID      *string             `json:"parent_id,omitempty"`
	State         protocol.IssueState `json:"state,omitempty"`
	IncludeClosed bool                `json:"include_closed,omitempty"`
	After         string              `json:"after,omitempty"`
	Limit         int                 `json:"limit,omitempty"`
}
type hostedIssueResourceArgs struct {
	IssueID         string `json:"issue_id,omitempty"`
	RuleID          string `json:"rule_id,omitempty"`
	After           string `json:"after,omitempty"`
	AfterSource     int64  `json:"after_source,omitempty"`
	AfterRevision   int64  `json:"after_revision,omitempty"`
	AfterOccurrence string `json:"after_occurrence,omitempty"`
}
type hostedSharedReviewReadArgs struct {
	IssueID    string `json:"issue_id,omitempty"`
	DeliveryID string `json:"delivery_id,omitempty"`
	After      string `json:"after,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}
type hostedDeploymentPreparationArgs struct {
	IssueID string `json:"issue_id,omitempty"`
}
type hostedRunnerReadArgs struct {
	HostID string `json:"host_id"`
}

type hostedAttentionReadArgs struct {
	AttentionID string `json:"attention_id"`
}

type hostedReadArgs struct {
	IssueID  string `json:"issue_id,omitempty"`
	Revision int64  `json:"revision,omitempty"`
}
type hostedEmptyPayload struct{}

type hostedMutation struct {
	name, description, target string
	payload                   any
	command                   app.Command
	main                      bool
}

var hostedMutations = []hostedMutation{
	{"decision_continuation_answer", "Record an actual owner answer with an explicit complete scheduling policy. Always use this typed operation for conditional worker handoffs; preserve source, parent Task revision and decision version. Omission never silently changes legacy answer semantics.", "decision", protocol.AnswerDecisionPayload{}, app.AnswerDecision{FromMessage: true, TypedContinuation: true}, true},
	{"decision_continuation_resume", "Independently resume the original answered worker question after an explicit wait. Read decision_continuation_read first; supply both original continuation and parent Task revisions and the actual admitted owner source. Never replace the answer, retry an unknown request under a new identity, or bypass stopped/current-contract guards.", "decision", protocol.ResumeDecisionContinuationPayload{}, app.ResumeDecisionContinuation{FromMessage: true}, true},
	{"decision_continuation_reconcile", "Reconcile readiness of the original answered worker question against exact dependency task/spec completion facts. Supply both original revisions. Caller readiness assertions, prose and elapsed time cannot satisfy the dependency; unknown remains unresolved. Reconciliation does not invent a new owner resume authorization.", "decision", protocol.ResumeDecisionContinuationPayload{}, app.ResumeDecisionContinuation{Reconcile: true, FromMessage: true}, true},
	{"runs_continue_retained", "Independently authorize only the original ordinary Orca Run after exact identity, protection-set and settled business receipt checks. Requires current Task revision and full immutable pin. Old runners remain retained; missing native identity cannot launch a replacement. Never use for managers or invent an owner decision.", "run", protocol.ContinueRetainedRunPayload{}, app.ContinueRetainedRun{}, true},
	{"artifact_shares_grant", "Share one immutable artifact and its exact files with a target Issue from an admitted owner authorization. No source task control or delegation of further sharing.", "issue", protocol.GrantArtifactSharePayload{}, app.ArtifactShareCommand{}, true},
	{"artifact_shares_revoke", "Revoke future reads of an exact artifact share using target Issue and share revisions. Previously frozen task inputs remain historical facts.", "issue", protocol.RevokeArtifactSharePayload{}, app.ArtifactShareCommand{Revoke: true}, true},
	{"question_inputs_grant", "Grant only selected file_ids from the actual owner answer or ReplyTo supplement to an exact worker question/run/lease. Use the input revision CAS, real source content revision and original request identity. Does not rewrite the answer, authorize other attachments, or inherit into a continuation. Return metadata then relay it to the original executor without restarting it.", "decision", protocol.QuestionInputPayload{}, app.QuestionInputCommand{}, true},
	{"question_inputs_revoke", "Revoke one exact question input grant_revision with the question input revision CAS. Stops future fetches; downloaded bytes, old answer receipts and independent spec inputs remain facts.", "decision", protocol.RevokeQuestionInputsPayload{}, app.QuestionInputCommand{Revoke: true}, true},
	{"tasks_reconcile_manager", "Reconcile only a proved-closed original manager after all native and business receipts are settled. Requires exact task revision, lease, close receipt SHA and current admitted owner source. Preserves waiting responsibility; never replay a missing session or declare its Issue complete.", "task", protocol.ReconcileManagerRunPayload{}, app.ReconcileManagerRun{}, true},
	{"attentions_resolve", "Handle an original problem assigned to this responsibility, preserving its exact question version. Use responsibility.revision as expected_control_revision and its assignment_epoch. For a worker handoff question include typed continuation continue_now, wait with reason, or dependency with reason and exact task/spec; omission preserves the legacy automatic handoff once answered and physically stopped under the current contract; use explicit continuation.mode=wait with a reason to hold continuation. Explain evidence/reason; this is an Agent answer within existing responsibility, never user authorization. Keep task effects and independent acceptance intact.", "attention", protocol.HandleAttentionPayload{}, app.HandleAttention{}, true},
	{"attentions_escalate", "Escalate an unresolved problem exactly one supervisor step: parent Issue, then main coordinator, then human. Use responsibility.revision and assignment_epoch. State attempted remedies and why they cannot work. Never notify the user directly for an internal problem, create duplicate problems, or erase spent attempts. Correct technical parameters and revise Agent-authored estimates within the existing goal; escalate when actual user scope, cost or attempt limits require it.", "attention", protocol.HandleAttentionPayload{}, app.HandleAttention{Escalate: true}, true},
	{"conversation_reply", "Reply to one admitted input. Tend selects its source endpoint; never repeat the body as native text.", "logical_turn", protocol.CoordinationReply{}, app.CoordinationReply{}, true},
	{"conversation_notify", "Publish a sourced proactive notification. Tend selects Web and Telegram; no recipient addresses are accepted.", "logical_turn", protocol.MessagingPublication{}, app.PublishCommunication{Notify: true}, true},

	{"issues_update_progress", "Maintain this Issue current progress and next step after findings, blockers or verified results. This assessment never changes task contracts or proves completion.", "issue", protocol.UpdateIssueProgressPayload{}, app.UpdateIssueProgress{}, false},
	{"issues_followups_create", "Arrange a bounded check of this Issue's existing authorized work; it creates no worker, new goal or execution authority. Deadline-owned rules use the deadline API.", "issue", protocol.IssueFollowupPayload{}, app.IssueFollowupCommand{Action: "create"}, false},
	{"issues_followups_update", "Revise this Issue's generic followup through its original rule revision; preserve unknown and previously claimed checks.", "followup", protocol.IssueFollowupPayload{}, app.IssueFollowupCommand{Action: "update"}, false},
	{"issues_followups_pause", "Pause this Issue's generic rule without erasing started checks.", "followup", protocol.IssueFollowupPayload{}, app.IssueFollowupCommand{Action: "pause"}, false},
	{"issues_followups_resume", "Resume this Issue's generic rule after rereading current facts; past time or expiry requires explicit update.", "followup", protocol.IssueFollowupPayload{}, app.IssueFollowupCommand{Action: "resume"}, false},
	{"issues_followups_cancel", "Cancel this Issue's generic rule while retaining original receipts and history.", "followup", protocol.IssueFollowupPayload{}, app.IssueFollowupCommand{Action: "cancel"}, false},
	{"issues_followups_trigger", "Request one bounded check of this Issue's generic rule; no new task or authority is implied.", "followup", protocol.IssueFollowupPayload{}, app.IssueFollowupCommand{Action: "trigger"}, false},
	{"issues_refresh_delivery", "Request a new provider observation for this Issue's linked delivery using the existing runner observer. Never merge, close or deploy.", "issue", protocol.IssueRefreshDeliveryPayload{}, app.IssueRefreshDelivery{}, false},
	{"consultation_promote", "Delegate an explicit owner execution request from this consultation by category to a runner Issue manager using only selected persisted source versions. Ordinary Q&A creates no task. Never execute native work.", "conversation", protocol.PromoteConsultationPayload{}, app.PromoteConsultation{}, false},
	{"runs_send_message", "Send supplemental evidence to this Issue primary task exact active run. It cannot revise its frozen contract or answer worker decisions; unknown is not resent.", "run", protocol.SendRunMessagePayload{}, app.SendRunMessage{}, false},
	{"issues_deadline_put", "Record a sourced deadline or estimate, with known/unknown time and default 24h/due reminders; never silently change a user commitment.", "deadline", protocol.PutIssueDeadlinePayload{}, app.IssueDeadlineCommand{}, true},
	{"issues_deadline_cancel", "Cancel a deadline with an explicit reason, preserving already claimed/unknown reminder facts.", "deadline", protocol.CancelIssueDeadlinePayload{}, app.IssueDeadlineCommand{Cancel: true}, true},
	{"planner_entry_put", "Record a user_action or issue_priority. Omit target_id when creating; on update provide target_id and expected_control_revision. The server assigns identity, revision and timestamps. This does not schedule an agent retry; task execution priority remains tasks_rate.", "planner_entry", protocol.PutPlannerEntryInput{}, app.PutPlannerEntry{}, true},
	{"attentions_create", "Submit a distinct question/action to your supervisor, or record nonblocking information. Agent-owned Issue questions follow the responsibility chain; only human-targeted problems enter user Inbox. Optional latest_action_at gives its true deadline with 24h/due reminders to main, even without an Issue. Assignment stays unchanged.", "attention", protocol.CreateAttentionPayload{}, app.AttentionCommand{Action: "create"}, true},
	{"attentions_update", "Revise this stable attention without replacing or answering its original decision. Omit latest_action_at to retain it, set 0 to clear; timezone only changes display.", "attention", protocol.UpdateAttentionPayload{}, app.AttentionCommand{Action: "update"}, true},
	{"attentions_respond", "Relay the owner's actual message to the original decision/version, including its typed worker-handoff continuation when supplied. Omission preserves the legacy automatic handoff once answered and physically stopped under the current contract; use explicit continuation.mode=wait with a reason to hold continuation. Free text never supplies typed continuation readiness. Never answer on the owner's behalf.", "attention", protocol.RespondAttentionPayload{}, app.AttentionCommand{Action: "respond", FromMessage: true}, true},
	{"attentions_confirm", "Relay an actual owner's confirmation of a human action; agent narration is insufficient.", "attention", protocol.ConfirmAttentionPayload{}, app.AttentionCommand{Action: "confirm", FromMessage: true}, true},
	{"attentions_withdraw", "Withdraw an ordinary open action/information with a reason; decisions retain their original business path.", "attention", hostedEmptyPayload{}, app.AttentionCommand{Action: "withdraw"}, true},
	{"conversation_associate_issues", "Classify this exact message into canonical Issues or explicitly leave it unassigned. Preserve owner corrections; this acknowledges routing only and never clears Issue source-review duties or authorizes execution.", "message", protocol.SetIssueMessageAssociationsPayload{}, app.SetIssueMessageAssociations{}, true},
	{"issues_set_acceptance_policy", "Only the original authenticated creator may change independent acceptance for future tasks using exact Issue revision/epoch. Investigations and read-only reports may waive a separate review; workers and current assignees cannot self-exempt. Existing task policies and pending reviews remain frozen.", "issue", protocol.SetIssueAcceptancePolicyPayload{}, app.SetIssueAcceptancePolicy{}, true},
	{"issues_create", "For an admitted deterministic rejection, set corrects_request_id to the original failed request when filling a missing plan title/intent; keep all established intent and source unchanged. Preserve unknown outcomes for reconciliation. Set independent_acceptance.required explicitly according to the work: investigations and read-only reports may omit a separate independent reviewer; include a reason. Missing policy preserves the independent-review requirement. The server freezes your creator identity; workers cannot change it. Create responsibility with owner.category_id and optional owner.agent_id. Any capable Agent can own an Issue. Owners create only direct children; another Agent needs its own child Issue.", "issue", protocol.CreateIssuePayload{}, app.CanonicalOwnerIssueCommand{Action: "create"}, true},
	{"issues_request_owner", "Assign continuing responsibility to an ordinary Agent by capability category, optionally agent_id. A child Issue is required when another Agent takes delegated work.", "issue", protocol.RequestIssueManagerPayload{}, app.RequestIssueOwner{}, true},
	{"issues_request_manager", "Publish a continuing Issue manager task by category. A matching runner claims it and becomes the assignee; no runner is selected at publication.", "issue", protocol.RequestIssueManagerPayload{}, app.RequestIssueManager{}, true},
	{"issues_assign", "Fence a concrete Issue assignee before reconciling and enabling its replacement. Does not stop independent workers.", "issue", protocol.IssueAssignmentPayload{}, app.HostedIssueAssignment{}, true},
	{"issues_finish_assignment", "Enable a fenced assignee only after the previous manager's actual operations are reconciled.", "issue", protocol.FinishIssueAssignmentPayload{}, app.HostedIssueAssignment{Finish: true}, true},
	{"issues_update", "Revise this Issue definition and coalesce a change notification to its current owner; task specifications stay frozen. The owner judges whether work still applies, needs revision or should pause. Persist continue_existing only with the observed expected_context_revision, each Task's expected_control_revision/spec_version and any required ancestor context_revisions; never automatically confirm every task merely because an Issue changed. Define pr_policy auto or manual for code delivery from real authenticated owner authority; deployment remains separate.", "issue", protocol.UpdateIssuePayload{}, app.CanonicalOwnerIssueCommand{Action: "update"}, false},
	{"issues_set_status", "Update this Issue's nonterminal work intent independently of worker execution. Use evidence-bearing close/cancel tools for endpoints. Does not wake itself.", "issue", protocol.SetIssueStatusPayload{}, app.CanonicalOwnerIssueCommand{Action: "set_status"}, false},
	{"issues_pause", "Stop new dispatch for this Issue; active worker contracts continue.", "issue", protocol.IssueControlPayload{}, app.CanonicalOwnerIssueCommand{Action: "pause"}, false},
	{"issues_resume", "Resume this Issue's new dispatch under its current assignment.", "issue", protocol.IssueControlPayload{}, app.CanonicalOwnerIssueCommand{Action: "resume"}, false},
	{"issues_cancel", "Record cancellation intent before settling outstanding tasks and obligations.", "issue", protocol.IssueControlPayload{}, app.CanonicalOwnerIssueCommand{Action: "cancel"}, false},
	{"issues_finish_cancel", "Finish cancellation with explicit source/follow-on dispositions after real execution has settled.", "issue", protocol.IssueEndpointPayload{}, app.CanonicalOwnerIssueCommand{Action: "finish_cancel"}, false},
	{"issues_close", "Close only this Issue after reviewing child/task results and obligations. For task results use kind acceptance with a passed acceptance_id, or kind result with the exact completed artifact_id only when its frozen creator policy waived independent acceptance; delivery requires a verified provider delivery observation, not an artifact_id. Owner evidence may reference this Issue's resolved decision/action Attention. Omit source_dispositions and expected_context_revision once source obligations have already been reviewed; context CAS accompanies actual source dispositions.", "issue", protocol.IssueEndpointPayload{}, app.CanonicalOwnerIssueCommand{Action: "close"}, false},
	{"issues_reuse", "Explicitly reuse this exact active/paused original Issue from its recorded creation request and current revision/epoch. Attach only the current admitted owner input with its source/content association revisions; preserve ownership, plan, terminal tasks and source review duties. Never infer duplicates from titles or replace an unknown creation request.", "issue", protocol.ReuseIssuePayload{}, app.ReuseIssue{}, true},
	{"issues_reopen", "Reopen from an exact current admitted owner source and restore continuing coordination. Preserve terminal implementation tasks; reopen grants no merge or deployment authority.", "issue", protocol.IssueControlPayload{}, app.CanonicalOwnerIssueCommand{Action: "reopen"}, true},
	{"issues_dependencies", "Revise this Issue's explicit completion dependencies without adding task execution ownership.", "issue", protocol.SetIssueDependenciesPayload{}, app.CanonicalOwnerIssueCommand{Action: "dependencies"}, false},
	{"issues_review_sources", "Review named late source shares on a terminal Issue while retaining its terminal history.", "issue", protocol.ReviewIssueSourcesPayload{}, app.CanonicalOwnerIssueCommand{Action: "review_sources"}, false},
	{"issues_update_delivery", "Retain accepted-output delivery responsibility. Prepare a concrete PR/head/base/target/checks and explicit merge/deployment scope. Under an authenticated Issue pr_policy auto, a valid shared whole-PR review permits merge without a synthetic human approval; manual mode retains its exact candidate approval. Deployment remains separate and requires its own concrete approval. For a live Task with a real registered candidate, prepare can establish its delivery responsibility without an intermediate artifact. After actual review and applicable approval, bind execution_task_id with expected_task_control_revision to continue on that same Task; a separate execution Task remains optional. Read delivery_authorization through normal Issue context for the exact current binding. Bounded reports have a separate endpoint. Complete historical delivery against the original frozen execution, real completion and independent acceptance, and its bound upgrade/installation receipts. Later independent maintenance or provider target/baseline drift does not require a new candidate, approval or reinstall merely to settle that history. Settlement grants no new merge/deploy/retry authority. New or unfinished execution still needs current admission and the applicable approval.", "issue", protocol.UpdateIssueDeliveryPayload{}, app.UpdateIssueDelivery{}, false},
	{"issues_review_delivery", "Share one formal reviewer and result across every Issue/task contributing to the same PR. Link/unlink the full delivery; start binds a real named reviewer_task_id, or acceptance_id imports a real prior acceptance whose immutable evidence.delivery_review binds the whole PR provider/repository/number/head/base/requirements_digest/repository_check. A plain task acceptance cannot become whole-PR approval. Supplement binds a real report Task by the original reviewer to add missing evidence once, retaining the original acceptance and round budget. For optional prebinding set reviewer_role=accept with the original source reviewer_task_id: freeze its actual acceptance Task/spec/artifact/reviewer/profile before a verdict. The acceptance reviewer reads read_observation shared_reviews and submits submit_verdict evidence.delivery_review; passed requires full PR evidence, changes_required retains actual findings for consolidated remediation, blocked/inconclusive remain waiting. A changes-required recheck on that same Task requires a genuinely new artifact and acceptance Run; waiting may resume on the same artifact through a new actual acceptance Run. Old runs and verdicts never spend another round. Omit reviewer_role for existing report/import behavior. Record reads the actual immutable acceptance or completed artifact.report.shared_review (review_id, head_sha, base_sha, requirements_digest, verdict, repository_check, findings), never an owner-supplied passed flag. Resolve every finding with fixed/not_adopted and reason; prefer one focused correction cycle for convergence; the current responsible owner may recheck further within existing scope and actual budget, preserving cost and prior rounds. authorize_recheck remains a legacy human-history operation, not a prerequisite for another round. correct reads reviewer_task_id as a real completed original-reviewer report, checks its original round/head/base/digest and appends an immutable correction without another formal round, replacing the old report, or reviving its Task. Original rounds, findings and authorization history remain. Assess confirms unchanged-candidate result applicability with an explicit reason without rerunning review. Read the returned candidate/requirement digest before creating the reviewer report.", "issue", protocol.UpdateSharedDeliveryReviewPayload{}, app.ReviewIssueDelivery{}, false},
	{"issues_review_completions", "Acknowledge exact primary task completion facts on this Issue without changing acceptance results.", "issue", protocol.ReviewIssueCompletionsPayload{}, app.CanonicalOwnerIssueCommand{Action: "review_completions"}, false},
	{"issues_create_task", "Arrange work in your Issue through a frozen task; self-execution keeps your same Agent identity; include an acceptance category only when its frozen creator policy requires a separate reviewer. Acceptance queues a contract; inspect execution state to establish worker startup. Choose another configured Agent/category for ordinary scoped collaboration in this Issue; create a child Issue when it needs independent continuing responsibility. independent_acceptance selects whether this new Task needs separate review; explicit human requirements and existing frozen Task policies remain binding. A waived review still requires a legal report, files or commit result and completion/delivery facts; native subagents cannot override policy. Within the existing user goal, arrange genuinely remaining same-Issue follow_up work with a reasonable bounded budget; an Agent-authored one-attempt estimate is not a permanent user cap. Preserve completed Tasks and spent attempts, actual user scope/cost/attempt limits, unresolved decisions and unknown effects.", "issue", protocol.CreateIssueTaskPayload{}, app.CanonicalOwnerTaskCreate{}, false},
	{"issues_update_task", "Explicitly refresh an existing primary task contract through its current revision and Issue epoch; active runs retain their frozen contracts. Correct a confirmed prelaunch defect here after reading runs[].preparation and reviewing inherited changes; an unchanged spec never retries work. Technical parameter or script corrections and reasonable future budgets are the responsible Agent's work within the existing goal; preserve spent attempts and actual user limits. Do not reopen a completed Task; use follow_up for genuine remaining work. A changed owner epoch or execution identity requires a new contract.", "task", protocol.UpdateIssueTaskPayload{}, app.CanonicalOwnerTaskUpdate{}, false},
	{"tasks_cancel", "Cancel only this Issue's primary task through its current revision and existing run reconciliation rules.", "task", hostedEmptyPayload{}, app.CancelTask{}, false},
	{"tasks_rate", "Set an explicitly selected primary task priority through its current revision; this does not change its contract.", "task", protocol.RateTaskPayload{}, app.RateTask{}, true},
	{"actions_create", "Request only an implement or independent acceptance generation for this Issue's primary task; native takeover and handback remain owner-only.", "task", protocol.CreateActionPayload{}, app.CreateAction{}, false},
	{"issues_context_fact", "Record or replace a sourced fact only in this Issue. Use observation for verified progress and operation receipts; reserve goal/constraint/decision/input for actual business requirements or sources. A changed digest asks the owner to assess meaning, not automatically revise a task or repeat a passed review. For an unchanged PR candidate whose original review still applies, use issues_review_delivery assess with a concrete reason. Original attribution is preserved; agent narration is not owner authority.", "issue", protocol.ContextFact{}, app.HostedIssueContextFact{}, false},
	{"issues_context_review", "Review only this Issue's exact source versions and context revision. Uncertainty and no new facts are valid; no global baseline or task-spec refresh is manufactured.", "issue", protocol.IssueContextReview{}, app.ReviewHostedIssueContext{}, false},
	{"issues_turn_commit", "Commit this manager's explicit current source/context review; native completion remains separate.", "logical_turn", protocol.LogicalTurnCommit{}, app.CommitLogicalTurn{}, false},
}

func hostedMutationSchema(payload any) json.RawMessage {
	var root, body map[string]any
	if err := json.Unmarshal([]byte(argumentSchema(hostedMutationArgs{})), &root); err != nil {
		panic(err)
	}
	if err := json.Unmarshal([]byte(argumentSchema(payload)), &body); err != nil {
		panic(err)
	}
	root["definitions"] = body["definitions"]
	if definitions, ok := body["definitions"].(map[string]any); ok {
		if workspace, ok := definitions["OwnerTaskWorkspace"].(map[string]any); ok {
			props := workspace["properties"].(map[string]any)
			props["mode"].(map[string]any)["description"] = "Omitted means new. Set reuse explicitly with source_task_id to continue a known same-Issue resource. Reuse preserves files but does not authorize committing inherited edits."
			props["source_task_id"].(map[string]any)["description"] = "Read the referenced source task and resource first; use only for explicit mode=reuse. A follow_up relation does not inherit its workspace or input files."
			props["include_existing_changes"].(map[string]any)["description"] = "For a reused commit task, set true only after establishing that inherited dirty edits belong to the existing authorized goal. Preserving files alone does not include them in commit scope. Inspect runs[].preparation on a confirmed prelaunch failure, then correct the same task with issues_update_task. Never absorb unrelated edits or blindly repeat the unchanged contract."
		}
	}
	delete(body, "definitions")
	root["properties"].(map[string]any)["payload"] = body
	root["properties"].(map[string]any)["effect_seq"].(map[string]any)["minimum"] = 1
	b, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return exportSchema(b)
}

// NamedSpecs is a role capability surface, not a category permission grant.
func NamedSpecs(purpose agentconfig.Purpose) []harness.ToolSpec {
	var out []harness.ToolSpec
	out = append(out, harness.ToolSpec{Name: "conversation_input_read", Description: "Read one exact admitted communication input and immediately record witnessed consumption. No address or channel selection.", InputSchema: json.RawMessage(`{"type":"object","properties":{"message_id":{"type":"string"}},"required":["message_id"],"additionalProperties":false}`)})
	if purpose == agentconfig.Main || purpose == agentconfig.IssueManager {
		out = append(out, harness.ToolSpec{Name: "issues_materials_read", Description: "Read saved Issue context and original materials. kind: overview (default Issue definitions), sources, facts, messages, tasks, artifacts, acceptances, questions, files, deliveries, reviews, deployment, capabilities, agent_capabilities, agent_capability_history or task_capabilities. id selects an exact object. For kind=reviews, id selects a delivery ID; after/limit page the shared review list. shared_reviews.existing_evidence is a bounded contributor evidence index with candidate identities and original artifact/file references, not complete reports or logs. Read needed originals with kind=artifacts and id=<artifact_id>, then file download; preserve missing evidence and truncation markers. sources preserves linked messages and queued/withdrawn/association states; facts includes superseded facts. File sources_more indicates truncated provenance. capabilities without id lists active/retired current definitions; with id returns that capability's history. Agent capability kinds use environment/agent as id; history requires it. task_capabilities requires a related Task ID and returns frozen requirements and actual matching facts. Use after/limit for complete pagination, retaining nested capability cursors and unknown states; preserve source, historical state, versions and original hashes. Omit issue_id for the current manager Issue. Reading does not admit owner instructions, change frozen inputs, schedule workers or authorize deployment.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedIssueMaterialsArgs{})))})
		out = append(out, harness.ToolSpec{Name: "decision_continuation_read", Description: "Read the original question's typed continuation policy, resume condition, blocked reason, both current revisions and any committed action/successor. Read-only ledger evidence; readiness does not prove worker startup.", InputSchema: exportSchema(json.RawMessage(argumentSchema(decisionContinuationReadArgs{})))})
		out = append(out, harness.ToolSpec{Name: "runs_maintenance_reconciliation", Description: "Read the original ordinary Run identity, maintenance protection set and unresolved receipt blockers without probing or controlling its provider.", InputSchema: exportSchema(json.RawMessage(argumentSchema(retainedRunReadArgs{})))})
	}
	if purpose == agentconfig.Main {
		out = append(out, harness.ToolSpec{Name: "tasks_manager_reconciliation", Description: "Read exact ledger-only original manager close, event and business receipt evidence. Missing sessions remain unknown; this read never authorizes native replay.", InputSchema: exportSchema(json.RawMessage(argumentSchema(managerReconciliationReadArgs{})))})
	}
	for _, m := range hostedMutations {
		if m.name == "issues_request_manager" {
			// Historical callbacks and the owner CLI retain their original
			// command. New named turns publish ordinary Agent ownership.
			continue
		}
		if (m.name == "consultation_promote") != (purpose == agentconfig.Consultation) && !communicationMutation(m.name) || purpose == agentconfig.Main && !m.main || purpose == agentconfig.IssueManager && m.main && !communicationMutation(m.name) && m.name != "issues_create" && m.name != "issues_request_owner" && m.name != "issues_set_acceptance_policy" && !strings.HasPrefix(m.name, "attentions_") && m.name != "runs_continue_retained" && m.name != "tasks_rate" && m.name != "issues_deadline_put" && m.name != "planner_entry_put" && !decisionContinuationMutation(m.name) {
			continue
		}
		spec := harness.ToolSpec{Name: m.name, Description: m.description, InputSchema: hostedMutationSchema(m.payload)}
		if decisionContinuationMutation(m.name) {
			spec.InputSchema = decisionContinuationMutationSchema(spec.InputSchema, m.name)
		}
		if m.name == "issues_reuse" || m.name == "issues_reopen" || m.name == "issues_create" || m.name == "issues_request_manager" || m.name == "issues_request_owner" {
			var root map[string]any
			_ = json.Unmarshal(spec.InputSchema, &root)
			body := root["properties"].(map[string]any)["payload"].(map[string]any)
			body["required"] = append(body["required"].([]any), "source_message_id")
			if m.name == "issues_reuse" {
				body["required"] = append(body["required"].([]any), "reason")
			}
			props := body["properties"].(map[string]any)
			if m.name == "issues_create" {
				delete(props, "manager")
			}
			if m.name == "issues_request_owner" {
				body["required"] = append(body["required"].([]any), "category_id")
			}
			props["source_message_id"] = map[string]any{"type": "string", "minLength": 1, "description": "Exact admitted message_id from conversation_turn_read. Never append @revision or substitute an attachment ID."}
			if m.name == "issues_reuse" || m.name == "issues_create" || m.name == "issues_reopen" {
				body["required"] = append(body["required"].([]any), "expected_source_revision")
				props["expected_source_revision"] = map[string]any{"type": "integer", "minimum": 1, "description": "Exact content_revision of source_message_id from conversation_turn_read; required with the source message ID."}
			}
			spec.InputSchema, _ = json.Marshal(root)
		}
		if m.name == "runs_continue_retained" && purpose == agentconfig.Main {
			var root map[string]any
			_ = json.Unmarshal(spec.InputSchema, &root)
			body := root["properties"].(map[string]any)["payload"].(map[string]any)
			body["required"] = append(body["required"].([]any), "source_message_id", "expected_source_revision")
			props := body["properties"].(map[string]any)
			props["source_message_id"] = map[string]any{"type": "string", "minLength": 1, "description": "Exact owner source admitted to this planner execution. Queued, withdrawn or merely visible material cannot authorize continuation."}
			props["expected_source_revision"] = map[string]any{"type": "integer", "minimum": 1, "description": "Exact content_revision of the admitted original owner source."}
			spec.InputSchema, _ = json.Marshal(root)
		}
		out = append(out, spec)
	}
	if purpose == agentconfig.Main {
		out = append(out, harness.ToolSpec{Name: "upgrades_start", Description: "Start an explicitly approved delivery's verified immutable local release asynchronously. park_idle_managers closes only idle manager processes under this window, preserving logical responsibility for new runs after completion/cancellation. Never wait inside the initiating worker or manager for its own installation. Unknown uses the same request_id/upgrade_id and original payload; read upgrade status through the CLI, never start a second window.", InputSchema: exportSchema(json.RawMessage(argumentSchema(protocol.UpgradeStartRequest{})))})
		out = append(out, harness.ToolSpec{Name: "issues_reuse_status", Description: "Read the exact original Issue creation receipt identity and current responsibility before an explicit reuse. Missing legacy evidence is unknown, not permission to recreate.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedIssueResourceArgs{})))})
	}
	if purpose == agentconfig.Main || purpose == agentconfig.IssueManager {
		out = append(out, harness.ToolSpec{Name: "artifact_shares_list", Description: "Read explicit artifact/file grants for the current Issue responsibility. Applicability is distinct from historical grants; reading grants no source Task access.", InputSchema: exportSchema(json.RawMessage(argumentSchema(artifactShareListArgs{})))})
		out = append(out, harness.ToolSpec{Name: "issues_list", Description: "Discover canonical Issues and responsibility hierarchy. Managers see only their Issue and descendants; listing grants no write permission.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedIssueListArgs{})))})
		for _, name := range []string{"issues_context_read", "issues_followups_list", "issues_followups_history", "issues_deliveries_read"} {
			description := "Read this canonical Issue's recorded context, followup history or delivery observations. Reading resolves nothing and never probes a provider."
			if name == "issues_context_read" {
				description = "Read this Issue's admitted source-review duties, owned coordination receipts and frozen task agreements. prepared reports the historical baseline; dispatch_ready is context diagnostics, not the admission result of issues_create_task. A missing legacy baseline never requires preparing global context for a canonical task. Use the formal task command with current Issue/source/assignment versions for the actual admission decision. Reading settles nothing."
			}
			schema := argumentSchema(hostedIssueResourceArgs{})
			if name == "issues_context_read" {
				description = "Read canonical Issue context. Prefer view=incremental: omit cursor for a paginated current snapshot, then retain next_cursor in this session for deltas. Follow more until the fixed event interval is complete; facts and source indexes are separate items. Current object versions may be newer than through and remain in the next interval. No read acknowledges sources or grants authority. Legacy mode uses after_source; historical numeric-string after is an equivalent alias, never ignored; both must agree. applied_after, next, more and content_id identify legacy progress. If the same cursor/content repeats, use the returned next position rather than repeating the old call. Cursor/scope errors require a fresh snapshot, not replaying actions."
				schema = argumentSchema(protocol.IssueContextQuery{})
			}
			out = append(out, harness.ToolSpec{Name: name, Description: description, InputSchema: exportSchema(json.RawMessage(schema))})
		}
		out = append(out, harness.ToolSpec{Name: "capabilities_list", Description: "Read versioned capability definitions; declarations and tool exposure do not prove execution success. No probing or writes.", InputSchema: exportSchema(json.RawMessage(argumentSchema(capabilityListArgs{})))})
		out = append(out, harness.ToolSpec{Name: "capabilities_read", Description: "Read one exact capability definition revision, or current when revision is omitted.", InputSchema: exportSchema(json.RawMessage(argumentSchema(capabilityReadArgs{})))})
		for _, name := range []string{"agents_capabilities_list", "agents_capabilities_read"} {
			out = append(out, harness.ToolSpec{Name: name, Description: "Read declared capabilities and ordinary executor availability; no writes. Agent requires environment; use after/limit for pages.", InputSchema: exportSchema(json.RawMessage(argumentSchema(protocol.AgentCapabilityQuery{})))})
		}
		out = append(out, harness.ToolSpec{Name: "tasks_capability_explain", Description: "Explain this Issue Task's frozen role requirements and actual scheduling eligibility without probing or allocating a lease.", InputSchema: exportSchema(json.RawMessage(argumentSchema(taskCapabilityArgs{})))})
		out = append(out, harness.ToolSpec{Name: "issues_delivery_reviews_read", Description: "Read this Issue or a descendant shared PR review, frozen candidate and requirements, actual reviewer rounds and result applicability. Optional delivery_id, after review-ID cursor and limit (default 50, maximum 100). Reading grants no control and never starts a review or probes a provider.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedSharedReviewReadArgs{})))})
		out = append(out, harness.ToolSpec{Name: "issues_deployment_preparation_read", Description: "Read the complete recorded deployment preparation for this Issue or a descendant: the fixed server, active hosts, runtime/control revisions, installation receipts, registered delivery facts and blockers, with source/time/sequence and explicit unknowns. Omit issue_id for the current manager Issue; Main must select an Issue. Reading grants no deployment authority, never refreshes a provider or schedules an upgrade, and accepts no host/provider selectors.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedDeploymentPreparationArgs{})))})
		out = append(out, harness.ToolSpec{Name: "agent_categories_list", Description: "Read the latest global Agent classifications; classification is not execution permission. Default active, limit 50, maximum 100.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedCategoryListArgs{})))})
		out = append(out, harness.ToolSpec{Name: "agents_runner_read", Description: "Read a runner host public registration, last executor observations and heartbeat presence separately. Reading neither probes nor changes runtime capability.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedRunnerReadArgs{})))})
	}
	if purpose != agentconfig.Consultation {
		for _, name := range []string{"attentions_list", "planner_read", "deadlines_list"} {
			out = append(out, harness.ToolSpec{Name: name, Description: "Read current person-facing obligations, next actions or deadlines with original identities; reading never resolves them.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedPlanningReadArgs{})))})
		}
		out = append(out, harness.ToolSpec{Name: "attentions_read", Description: "Read the same original decision/answer and attention identity used by owner Inbox.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedAttentionReadArgs{})))})
	}
	out = append(out, harness.ToolSpec{Name: "issues_read", Description: "Read an Issue and its primary execution/completion facts; managers are limited to their Issue and descendants.", InputSchema: exportSchema(json.RawMessage(argumentSchema(hostedReadArgs{})))})
	out = append(out, harness.ToolSpec{Name: "agents_hosted_list", Description: "Read accepted hosted identities, roles and configuration revision. Private instructions and local credentials are not exposed.", InputSchema: exportSchema(json.RawMessage(argumentSchema(turnReadArgs{})))})
	if purpose == agentconfig.IssueManager {
		out = append(out, harness.ToolSpec{Name: "download_file", Description: "Download an Issue attachment or evidence file by file_id to the runner private directory, verifying original size and SHA256. Returns only the runner-local path.", InputSchema: json.RawMessage(`{"type":"object","properties":{"file_id":{"type":"string"}},"required":["file_id"],"additionalProperties":false}`)})
		out = append(out, harness.ToolSpec{Name: "issues_turn_read", Description: "Read this manager turn's exact source versions and context share.", InputSchema: exportSchema(json.RawMessage(argumentSchema(turnReadArgs{})))})
	}
	for _, spec := range Specs() {
		if namedLegacyRead(purpose, spec.Name) {
			if purpose == agentconfig.Main && spec.Name != "background_sources" && spec.Name != "background_checkin" {
				spec = HostedOwnerSourceSpec(spec)
			}
			out = append(out, spec)
		}
	}
	return out
}
func communicationMutation(name string) bool {
	return name == "conversation_reply" || name == "conversation_notify"
}

func namedLegacyRead(purpose agentconfig.Purpose, name string) bool {
	if purpose == agentconfig.Consultation {
		return name == "conversation_read"
	}
	if purpose == agentconfig.Main {
		switch name {
		case "conversation_handoff_audit", "tend_context", "repos_list", "tasks_list", "tasks_read", "actions_list", "runs_read", "artifacts_read", "decisions_list", "decisions_read", "decisions_resolve", "decisions_explain", "question_inputs_read", "conversation_read", "conversation_turn_read", "conversation_turn_commit", "conversation_turn_wait", "conversation_turn_adopt", "agent_conversations_list", "agent_rollout_read", "background_sources", "background_checkin":
			return true
		}
	}
	if purpose == agentconfig.IssueManager {
		switch name {
		case "runs_read_messages", "repos_list", "tasks_read", "actions_list", "runs_read", "artifacts_read", "background_evidence_read", "background_sources", "agent_rollout_read", "conversation_read":
			return true
		}
	}
	return false
}
func hostedAnswer(value any, err error) harness.ToolResult {
	out := answer{OK: err == nil, Result: value}
	if err != nil {
		if !errors.As(err, &out.Error) {
			out.Error = protocol.Errorf(protocol.CodeInternal, "hosted tool failed")
		}
	}
	b, e := json.Marshal(out)
	if e != nil {
		return harness.ToolResult{Text: "Cannot encode hosted tool result"}
	}
	return harness.ToolResult{Success: out.OK, Text: string(b)}
}
func (s *Service) handleHosted(ctx context.Context, c Call) (harness.ToolResult, bool) {
	execution, found, err := s.app.HostedExecution(ctx, c.executionRequest())
	if err != nil {
		return hostedAnswer(nil, err), true
	}
	if !found {
		return harness.ToolResult{}, false
	}
	fail := func(err error) (harness.ToolResult, bool) { return hostedAnswer(nil, err), true }
	if execution.Selection.RunnerManaged {
		if err := s.app.AuthorizeRunnerCoordination(ctx, c.Principal, execution); err != nil {
			return fail(err)
		}
	} else if c.Principal.Kind == protocol.PrincipalRun {
		return fail(protocol.Errorf(protocol.CodeForbidden, "run has no hosted coordinator authority"))
	}
	var native store.MainTurn
	err = s.store.Read(ctx, func(tx *store.ReadTx) error {
		var e error
		native, e = store.GetMainTurn(ctx, tx.Q(), execution.ID)
		return e
	})
	if err != nil {
		return fail(err)
	}
	if native.ThreadID != c.Principal.Thread || native.TurnID != c.TurnID || execution.ConversationID != store.ConversationID(c.Principal.ConversationID) {
		return fail(protocol.Errorf(protocol.CodeForbidden, "caller does not own the admitted execution"))
	}
	repair, err := s.app.IsRepairExecution(ctx, execution.ID)
	if err != nil {
		return fail(err)
	}
	if repair && !namedRepairTool(execution.Selection.Purpose, c.Tool) {
		return fail(protocol.Errorf(protocol.CodeForbidden, "metadata repair cannot perform business operations"))
	}
	if c.Tool == "upgrades_start" {
		if execution.Selection.Purpose != agentconfig.Main {
			return fail(protocol.Errorf(protocol.CodeForbidden, "only the admitted main coordinator may start a delivery upgrade"))
		}
		c.ExecutionRequestID = execution.ID
		value, err := s.startHostedUpgrade(ctx, c, native.Status == store.TurnStarting || native.Status == store.TurnStarted)
		return hostedAnswer(value, err), true
	}
	if c.Tool == "decision_continuation_read" {
		var args decisionContinuationReadArgs
		if err := decode(c.Arguments, &args); err != nil {
			return fail(err)
		}
		value, err := s.readDecisionContinuation(ctx, execution, args.DecisionID)
		return hostedAnswer(value, err), true
	}
	if c.Tool == "runs_maintenance_reconciliation" {
		if execution.Selection.Purpose != agentconfig.Main && execution.Selection.Purpose != agentconfig.IssueManager {
			return fail(protocol.Errorf(protocol.CodeForbidden, "only an admitted coordinator can inspect retained Run reconciliation"))
		}
		var args retainedRunReadArgs
		if err := decode(c.Arguments, &args); err != nil {
			return fail(err)
		}
		value, err := s.app.RetainedRunReconciliation(ctx, args.RunID)
		if err == nil && execution.Selection.Purpose == agentconfig.IssueManager {
			err = s.store.Read(ctx, func(tx *store.ReadTx) error {
				primary, e := store.GetTaskIssueOwner(ctx, tx.Q(), value.Identity.TaskID)
				if e != nil {
					return e
				}
				if primary != execution.IssueID {
					return protocol.Errorf(protocol.CodeForbidden, "retained Run is outside this Issue")
				}
				current, e := store.GetCanonicalIssue(ctx, tx.Q(), primary)
				if e != nil {
					return e
				}
				if current.AssignmentEpoch != execution.AssignmentEpoch || current.AssignmentState != "active" {
					return protocol.Errorf(protocol.CodeForbidden, "Issue responsibility changed")
				}
				return nil
			})
		}
		if err != nil {
			return fail(err)
		}
		return hostedAnswer(value, nil), true
	}
	if c.Tool == "tasks_manager_reconciliation" {
		if execution.Selection.Purpose != agentconfig.Main {
			return fail(protocol.Errorf(protocol.CodeForbidden, "only the main coordinator can inspect global manager reconciliation"))
		}
		value, err := s.tasksManagerReconciliation(ctx, c)
		return hostedAnswer(value, err), true
	}
	if c.Tool == "conversation_input_read" {
		var args struct {
			MessageID string `json:"message_id"`
		}
		if err := decode(c.Arguments, &args); err != nil {
			return fail(err)
		}
		value, err := s.app.ReadCommunicationInput(ctx, execution.ID, args.MessageID)
		return hostedAnswer(value, err), true
	}
	for _, m := range hostedMutations {
		if c.Tool != m.name {
			continue
		}
		if (m.name == "consultation_promote") != (execution.Selection.Purpose == agentconfig.Consultation) && !communicationMutation(m.name) || execution.Selection.Purpose == agentconfig.Main && !m.main {
			return fail(protocol.Errorf(protocol.CodeForbidden, "this role must delegate Issue execution"))
		}
		var args hostedMutationArgs
		if err := decode(c.Arguments, &args); err != nil {
			return fail(err)
		}
		if c.Tool == "consultation_promote" && args.TargetID == "" {
			args.TargetID = execution.ConversationID
		}
		if c.Tool == "attentions_create" && execution.IssueID != "" {
			var p protocol.CreateAttentionPayload
			if err := json.Unmarshal(args.Payload, &p); err != nil {
				return fail(protocol.Errorf(protocol.CodeInvalidRequest, "invalid attention payload"))
			}
			if p.IssueID == "" {
				p.IssueID = execution.IssueID
			}
			args.Payload, _ = json.Marshal(p)
		}
		command := m.command
		if c.Tool == "issues_turn_commit" {
			command = app.CommitLogicalTurn{RequestID: execution.ID}
			if args.TargetID == "" {
				args.TargetID = execution.ID
			}
		}
		if m.target == "issue" && args.TargetID == "" && c.Tool != "issues_create" {
			args.TargetID = execution.IssueID
		}
		cmd := app.HostedCoordinationCommand{Command: command, ExecutionID: execution.ID, EffectSeq: args.EffectSeq}
		request := c.RequestID
		if request == "" {
			if c.Native == "" {
				return fail(protocol.Errorf(protocol.CodeMissingRequestID, "tool callback has no native identity"))
			}
			var err error
			request, err = s.app.NativeRequestID(ctx, "harness:"+c.Principal.Subject, c.Native)
			if err != nil {
				return fail(err)
			}
		}
		env := &protocol.Envelope{RequestID: request, Operation: command.Operation(), Target: protocol.Target{Type: m.target, ID: args.TargetID}, ExpectedControlRevision: args.ExpectedControlRevision, Reason: args.Reason, Payload: args.Payload}
		result, err := s.app.Execute(ctx, c.Principal, env, cmd)
		receipt := map[string]any{"request_id": request}
		if result.RequestID != "" {
			receipt["request_id"] = result.RequestID
		}
		if result.Status != "" {
			receipt["status"] = result.Status
		}
		if err != nil {
			return hostedAnswer(receipt, err), true
		}
		receipt["outcome"] = result.Result
		if result.Error != nil {
			return hostedAnswer(receipt, result.Error), true
		}
		return hostedAnswer(receipt, nil), true
	}
	if c.Tool == "download_file" && execution.Selection.RunnerManaged {
		var args struct {
			FileID string `json:"file_id"`
		}
		if err := decode(c.Arguments, &args); err != nil {
			return fail(err)
		}
		file, err := s.app.ManagerFile(ctx, c.Principal, args.FileID)
		return hostedAnswer(file, err), true
	}
	if c.Tool == "attentions_list" || c.Tool == "planner_read" || c.Tool == "deadlines_list" {
		if execution.Selection.Purpose == agentconfig.Consultation {
			return fail(protocol.Errorf(protocol.CodeForbidden, "consultation has no planner tools"))
		}
		var p hostedPlanningReadArgs
		if err := decode(c.Arguments, &p); err != nil {
			return fail(err)
		}
		if execution.IssueID != "" {
			if p.IssueID != "" && p.IssueID != execution.IssueID {
				return fail(protocol.Errorf(protocol.CodeForbidden, "planning read is outside this Issue"))
			}
			p.IssueID = execution.IssueID
		}
		var value any
		err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			switch c.Tool {
			case "attentions_list":
				value, err = views.Attentions(ctx, tx.Q(), views.AttentionFilter{ResponsibleIssueID: p.IssueID, Recipient: responsibilityReadRecipient(execution.Selection.Purpose), Kind: p.Kind, Status: p.Status, After: p.After})
			case "planner_read":
				value, err = views.NextActions(ctx, tx.Q(), p.Kind, p.IssueID)
			case "deadlines_list":
				value, err = views.Deadlines(ctx, tx.Q(), p.IssueID)
			}
			return err
		})
		return hostedAnswer(value, err), true
	}
	if c.Tool == "attentions_read" {
		if execution.Selection.Purpose == agentconfig.Consultation {
			return fail(protocol.Errorf(protocol.CodeForbidden, "consultation has no planner tools"))
		}
		var p struct {
			AttentionID string `json:"attention_id"`
		}
		if err := decode(c.Arguments, &p); err != nil {
			return fail(err)
		}
		var value protocol.AttentionView
		err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			value, err = views.Attention(ctx, tx.Q(), p.AttentionID)
			if err == nil && execution.IssueID != "" && value.IssueID != execution.IssueID && (value.Responsibility == nil || value.Responsibility.IssueID != execution.IssueID && value.Responsibility.OriginIssueID != execution.IssueID) {
				return protocol.Errorf(protocol.CodeForbidden, "attention is outside this Issue")
			}
			return err
		})
		return hostedAnswer(value, err), true
	}
	if result, handled := s.hostedCapabilities(ctx, c, execution); handled {
		return result, true
	}
	if c.Tool == "agent_categories_list" || c.Tool == "agents_runner_read" {
		if execution.Selection.Purpose != agentconfig.Main && execution.Selection.Purpose != agentconfig.IssueManager {
			return fail(protocol.Errorf(protocol.CodeForbidden, "catalog is available to the planner and Issue manager"))
		}
		if c.Tool == "agent_categories_list" {
			var p hostedCategoryListArgs
			if err := decode(c.Arguments, &p); err != nil {
				return fail(err)
			}
			v, err := s.app.AgentCategories(ctx, p.State, p.After, p.Limit)
			return hostedAnswer(v, err), true
		}
		var p hostedRunnerReadArgs
		if err := decode(c.Arguments, &p); err != nil {
			return fail(err)
		}
		if p.HostID == "" {
			return fail(protocol.Errorf(protocol.CodeInvalidRequest, "host_id is required"))
		}
		v, err := s.app.RunnerAgentCatalog(ctx, p.HostID)
		return hostedAnswer(v, err), true
	}
	if result, handled := s.hostedArtifactShares(ctx, c, execution); handled {
		return result, true
	}
	if result, handled := s.canonicalIssueRead(ctx, c, execution); handled {
		return result, true
	}
	if c.Tool == "agents_hosted_list" {
		v, e := s.app.HostedAgentInventory(ctx)
		return hostedAnswer(v, e), true
	}
	if c.Tool == "issues_turn_read" && execution.IssueID != "" {
		v, e := s.app.LogicalTurn(ctx, execution.ID)
		return hostedAnswer(v, e), true
	}
	if c.Tool == "issues_read" {
		var p hostedReadArgs
		if err := decode(c.Arguments, &p); err != nil {
			return fail(err)
		}
		if p.IssueID == "" {
			p.IssueID = execution.IssueID
		}
		var value any
		err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			if execution.IssueID != "" {
				var allowed bool
				if err := tx.Q().QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (SELECT ? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN descendants d ON c.parent_issue_id=d.id) SELECT EXISTS(SELECT 1 FROM descendants WHERE id=?)`, execution.IssueID, p.IssueID).Scan(&allowed); err != nil {
					return err
				}
				if !allowed {
					return protocol.Errorf(protocol.CodeForbidden, "Issue is outside manager scope")
				}
			}
			issue, e := views.CanonicalIssue(ctx, tx.Q(), p.IssueID, p.Revision)
			if e != nil {
				return e
			}
			visible := issue.SourceReviews[:0]
			for _, source := range issue.SourceReviews {
				admitted, err := store.OwnerInputAdmitted(ctx, tx.Q(), source.MessageID)
				if err != nil {
					return err
				}
				if !admitted {
					continue
				}
				item := protocol.ConversationItem{ConversationContent: protocol.ConversationContent{MessageID: source.MessageID, Author: source.Author, Body: source.Body}}
				if err := store.HideUndeliveredOwner(ctx, tx.Q(), &item); err != nil {
					return err
				}
				source.Body = item.Body
				visible = append(visible, source)
			}
			issue.SourceReviews = visible
			value = issue
			return nil
		})
		return hostedAnswer(value, err), true
	}
	if execution.Selection.Purpose == agentconfig.Consultation && c.Tool == "conversation_read" {
		var p conversationArgs
		if err := decode(c.Arguments, &p); err != nil {
			return fail(err)
		}
		if p.ConversationID != "" && p.ConversationID != execution.ConversationID {
			return fail(protocol.Errorf(protocol.CodeForbidden, "read only this consultation"))
		}
		p.ConversationID = execution.ConversationID
		c.Arguments, _ = json.Marshal(p)
	}
	if !namedLegacyRead(execution.Selection.Purpose, c.Tool) {
		return fail(protocol.Errorf(protocol.CodeForbidden, "tool is unavailable to this named role"))
	}
	if execution.IssueID != "" {
		if c.Tool == "conversation_read" {
			var p conversationArgs
			if err := decode(c.Arguments, &p); err != nil {
				return fail(err)
			}
			if p.ConversationID != "" && p.ConversationID != execution.ConversationID {
				return fail(protocol.Errorf(protocol.CodeForbidden, "conversation is outside this Issue"))
			}
			p.ConversationID = execution.ConversationID
			c.Arguments, _ = json.Marshal(p)
		}
		var ids struct {
			TaskID     string `json:"task_id"`
			RunID      string `json:"run_id"`
			ArtifactID string `json:"artifact_id"`
		}
		_ = json.Unmarshal(c.Arguments, &ids)
		err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			task := ids.TaskID
			if ids.RunID != "" {
				run, e := store.GetRun(ctx, tx.Q(), ids.RunID)
				if e != nil {
					return e
				}
				task = run.TaskID
			}
			if ids.ArtifactID != "" {
				artifact, e := store.GetArtifact(ctx, tx.Q(), ids.ArtifactID)
				if e != nil {
					return e
				}
				task = artifact.TaskID
			}
			if task != "" {
				allowed, e := hostedTaskResultReadable(ctx, tx.Q(), task, execution.IssueID)
				if e != nil {
					return e
				}
				if !allowed && c.Tool == "artifacts_read" && ids.ArtifactID != "" && ids.TaskID == "" && ids.RunID == "" {
					allowed, e = store.ArtifactShareReadable(ctx, tx.Q(), execution.IssueID, ids.ArtifactID, execution.AssignmentEpoch)
					if e != nil {
						return e
					}
				}
				if !allowed {
					return protocol.Errorf(protocol.CodeForbidden, "task execution facts are outside this Issue")
				}
			}
			return nil
		})
		if err != nil {
			return fail(err)
		}
	}
	c.hostedRead = true
	return s.Handle(ctx, c), true
}

// Repair capabilities remain a subset of the admitted role's tool surface.
func namedRepairTool(purpose agentconfig.Purpose, name string) bool {
	switch name {
	case "conversation_associate_issues":
		return purpose == agentconfig.Main
	case "issues_read", "agents_hosted_list":
		return true
	case "issues_turn_read", "issues_context_fact", "issues_context_review", "issues_turn_commit":
		return purpose == agentconfig.IssueManager
	default:
		return repairTools[name] && namedLegacyRead(purpose, name)
	}
}

// hostedTaskResultReadable adds delegated descendant results to the existing
// explicitly linked evidence scope. It never grants task or source control.
func hostedTaskResultReadable(ctx context.Context, q store.Querier, taskID, issueID string) (bool, error) {
	linked, err := store.TaskIssueEvidenceReadable(ctx, q, taskID, issueID)
	if err != nil || linked {
		return linked, err
	}
	var descendant bool
	err = q.QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (
 SELECT issue_id FROM issue_controls WHERE parent_issue_id=?
 UNION ALL SELECT c.issue_id FROM issue_controls c JOIN descendants d ON c.parent_issue_id=d.id)
 SELECT EXISTS(SELECT 1 FROM task_work_item_owners o JOIN descendants d ON d.id=o.work_item_id WHERE o.task_id=?)`, issueID, taskID).Scan(&descendant)
	return descendant, err
}

func responsibilityReadRecipient(purpose agentconfig.Purpose) string {
	if purpose == agentconfig.Main {
		return "all"
	}
	return "agent"
}
