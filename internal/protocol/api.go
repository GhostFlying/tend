package protocol

// APITypes are the root types of the owner API's JSON Schema, from which
// clients' types are generated (D22). Types they refer to are
// included through them.
func APITypes() []any {
	return []any{Envelope{}, APIResponse{}, Snapshot{}, TaskPage{}, TaskDetail{}, RunLogPage{}, ConversationPage{}, LedgerEvent{}, StreamStart{}, ArtifactDetail{}, RequestView{},
		SessionRequest{}, SessionView{}, InterruptReplyPayload{}, SubmitMessagePayload{}, AnswerDecisionPayload{}, AnswerResult{}, SetControlModePayload{}, RateTaskPayload{}, CreateActionPayload{},
		ConversationSettingsView{}, SetConversationSettingsPayload{},
		ArtifactPayload{}, HookRequest{}, HookResponse{}, ToolSpec{}, ToolRequest{}, ToolResponse{}}
}

// APIEnums lists the values of every string enum in the owner API.
func APIEnums() []any {
	return []any{
		[]TaskRelationKind{RelationFollowUp, RelationRelated},
		[]RelationDirection{RelationOutgoing, RelationIncoming},
		[]TaskLifecycle{TaskDraft, TaskQueued, TaskRunning, TaskAwaitingAcceptance, TaskChangesRequired, TaskCompleted, TaskCancelled},
		[]ControlMode{ControlManaged, ControlPaused, ControlHumanControl},
		[]Rating{RatingNone, RatingP0, RatingP1, RatingP2, RatingP3},
		[]BlockReason{BlockNone, BlockWaitingResource, BlockWaitingDecision, BlockWaitingApproval, BlockAcceptanceEnvUnavailable, BlockBudgetExhausted, BlockWorkspaceUnavailable},
		[]RunObserved{RunStarting, RunRunning, RunWaiting, RunFinished, RunFailed, RunInterrupted, RunUnknown},
		[]RunDesired{DesiredRun, DesiredPaused, DesiredStopped, DesiredHeld},
		[]MessageState{MessageQueued, MessageProcessing, MessageReplied, MessageInterrupted, MessageFailed, MessageUnknown},
		[]AssistantState{AssistantStreaming, AssistantCompleted, AssistantInterrupted, AssistantFailed, AssistantUnknown},
		[]ToolStatus{ToolRunning, ToolCompleted, ToolFailed, ToolInterrupted},
		[]RunRole{RoleImplement, RoleAccept},
		[]ActionKind{ActionImplement, ActionAccept, ActionDeliver},
		[]DecisionEffect{EffectImplement, EffectAccept, EffectCancel, EffectStartOver},
		[]ActionStatus{ActionPending, ActionReady, ActionDispatched, ActionDone, ActionCancelled, ActionSuperseded},
		[]Verdict{VerdictPassed, VerdictChangesRequired, VerdictBlocked, VerdictInconclusive},
		[]Code{CodeInvalidRequest, CodeMissingRequestID, CodeConflictPayload, CodeStaleRevision, CodeUnauthenticated, CodeForbidden, CodeNotFound,
			CodeInvalidTransition, CodeDuplicateEffect, CodeUnknownOutcome, CodeInternal},
		[]OpStatus{OpSucceeded, OpFailed, OpAccepted, OpUnknown},
		[]TokenScope{ScopeOwner, ScopeAgent},
		Agents,
		[]HookEvent{HookSessionStart, HookUserPromptSubmit},
		WorkerKinds,
	}
}
