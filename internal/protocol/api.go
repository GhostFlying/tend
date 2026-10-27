package protocol

// APITypes are the root types of the owner API's JSON Schema, from which
// clients' types are generated (D22). Types they refer to are
// included through them.
func APITypes() []any {
	return []any{
		RepositoryRegistration{}, RegisterRepositoryPayload{}, RunnerRepository{}, RunRolloutPage{}, TokenUsageSample{}, TokenUsageView{},
		AgentRolloutPage{}, AgentRolloutWindow{}, AgentConversation{}, AgentConversationPage{}, MessageAssociations{}, SetMessageAssociationsPayload{}, WorkItemContext{}, ContextFact{}, PrepareWorkItemContextPayload{}, BackgroundAssessmentPayload{}, AgentContextUsage{}, AgentCompactionPage{}, BackgroundSourcePage{}, CoordinationMetrics{}, FollowupPage{}, FollowupHistoryPage{}, FollowupSpec{}, UpdateFollowupPayload{}, ConversationQueuePage{}, TranscriptPage{}, TranscriptBlock{}, WithdrawMessagePayload{}, WithdrawMessageResult{}, Envelope{}, APIResponse{}, Snapshot{}, RepoRegistry{}, TaskPage{}, TaskDetail{}, RunLogPage{}, ConversationPage{}, LedgerEvent{}, StreamStart{}, ArtifactDetail{}, RequestView{},
		WorkItemChoice{}, WorkItemChoicePage{}, CreateWorkItemChoicePayload{}, UpdateWorkItemChoicePayload{}, RefreshDeliveryPayload{}, WorkItemPage{}, WorkItemView{}, CreateWorkItemPayload{}, UpdateWorkItemPayload{}, CloseWorkItemPayload{}, ReopenWorkItemPayload{}, LinkWorkItemTaskPayload{},
		SessionRequest{}, SessionView{}, InterruptReplyPayload{}, SubmitMessagePayload{}, AnswerDecisionPayload{}, AnswerResult{}, SetControlModePayload{}, RateTaskPayload{}, CreateActionPayload{},
		ConversationAttachment{}, DownloadURLResponse{}, ConversationImageResponse{}, ConversationSettingsView{}, SetConversationSettingsPayload{}, ConversationHandoffView{}, SetConversationHandoffSettingsPayload{}, ConversationHandoffPayload{},
		ExplainDecisionPayload{}, ArtifactPayload{}, ToolSpec{}}
}

// APIEnums lists the values of every string enum in the owner API.
func APIEnums() []any {
	return []any{
		ExecutionStateValues(), QueueWaitCodeValues(),
		SharedFactKindValues(), WorkItemStatusValues(), WorkItemUpdateStatusValues(),
		WorkItemStageStatusValues(), WorkItemFollowOnKindValues(), WorkItemFollowOnStatusValues(),
		WorkItemDeliveryProviderValues(), WorkItemEvidenceKindValues(), WorkItemClosureKindValues(), WorkItemTaskDispositionActionValues(),
		[]TaskRelationKind{RelationFollowUp, RelationRelated},
		[]RelationDirection{RelationOutgoing, RelationIncoming},
		[]TaskLifecycle{TaskDraft, TaskQueued, TaskRunning, TaskAwaitingAcceptance, TaskChangesRequired, TaskCompleted, TaskCancelled},
		[]ControlMode{ControlManaged, ControlPaused, ControlHumanControl},
		[]Rating{RatingNone, RatingP0, RatingP1, RatingP2, RatingP3},
		[]BlockReason{BlockNone, BlockWaitingResource, BlockWaitingDecision, BlockWaitingApproval, BlockAcceptanceEnvUnavailable, BlockBudgetExhausted, BlockWorkspaceUnavailable},
		[]RunObserved{RunStarting, RunRunning, RunWaiting, RunFinished, RunFailed, RunInterrupted, RunUnknown},
		[]RunDesired{DesiredRun, DesiredPaused, DesiredStopped, DesiredHeld},
		[]MessageState{MessageQueued, MessageAccepted, MessageProcessed, MessageProcessing, MessageReplied, MessageInterrupted, MessageFailed, MessageUnknown},
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
		[]TokenScope{ScopeOwner},
		Agents,
		WorkerKinds,
	}
}
