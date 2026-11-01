package protocol

// APITypes are the root types of the owner API's JSON Schema, from which
// clients' types are generated (D22). Types they refer to are
// included through them.
func APITypes() []any {
	return []any{
		HostMaintenancePayload{}, UpgradeControlPayload{}, LocalRepositoryBinding{}, BindLocalRepositoryRequest{}, LocalRepositoryStatus{},
		TriggerQuery{}, TriggerEventPage{}, TriggerSchedulePage{}, WebhookSubscription{}, WebhookSubscriptionPage{}, WebhookSubscriptionPayload{}, WebhookReceipt{},
		MessagingEvent{}, MessagingChannel{}, MessagingInput{}, MessagingPublication{}, BindTelegram{}, MessagingReadPayload{}, MessagingMessage{}, MessagingPage{}, MessagingRead{}, MessagingDelivery{}, MessagingReceipts{}, MessagingTrace{}, MessagingInteraction{}, RespondInteraction{},
		IssueBlocker{}, SetIssueStatusPayload{}, IssueNotificationSettings{}, SetIssueNotificationSettingsPayload{},
		ManagerMigrationPayload{}, RequestIssueManagerPayload{}, IssueManagerView{}, InjectSourcePayload{}, CreateConsultationPayload{}, PromoteConsultationPayload{}, ManualSource{}, NativeEntry{}, SendRunMessagePayload{}, RunMessagePage{},
		ArchiveControlRequest{}, ArchiveHostControlRequest{}, ArchiveOnlineStatus{},
		ArchiveCatalog{}, ArchiveSummary{}, ArchiveRecord{}, ArchiveRecordsPage{}, ArchiveOriginal{}, ArchiveOriginalPage{}, ArchiveResource{}, ArchiveResourcePage{},
		AttentionView{}, AttentionPage{}, CreateAttentionPayload{}, UpdateAttentionPayload{}, RespondAttentionPayload{}, ConfirmAttentionPayload{}, PresentAttentionPayload{}, IssueDeadline{}, PutIssueDeadlinePayload{}, CancelIssueDeadlinePayload{}, PlannerEntry{}, PutPlannerEntryPayload{}, NextActionPage{},
		IssueActivityPage{}, IssueResultPage{}, UpdateIssueProgressPayload{}, ExecutionCounts{}, IssueView{}, IssueContextView{}, IssueFollowupPayload{}, IssueFollowupPage{}, IssueFollowupHistory{}, IssueDeliveryPage{}, IssueRefreshDeliveryPayload{}, IssuePage{}, CreateIssuePayload{}, UpdateIssuePayload{}, IssueControlPayload{}, IssueEndpointPayload{}, ReparentIssuePayload{}, SetIssueDependenciesPayload{}, ReviewIssueSourcesPayload{}, ReviewIssueCompletionsPayload{}, CreateIssueTaskPayload{}, UpdateIssueTaskPayload{},
		HostedAgentInventory{}, AgentCategory{}, PutAgentCategoryPayload{}, AgentCategoryPage{}, RunnerAgentCatalog{}, TaskAgentSelection{}, RunExecutionPin{}, AgentExecutor{}, IssueAssignmentPayload{}, FinishIssueAssignmentPayload{}, IssueContextReview{},
		RepositoryRegistration{}, RegisterRepositoryPayload{}, RunnerRepository{}, RunRolloutPage{}, TokenUsageSample{}, TokenUsageView{},
		AgentRolloutPage{}, AgentRolloutWindow{}, IssueAgentConversation{}, IssueAgentConversationPage{}, IssueConversationPage{}, IssueConversationItem{}, IssueMessageAssociations{}, SetIssueMessageAssociationsPayload{}, AgentContextUsage{}, AgentCompactionPage{}, BackgroundSourcePage{}, CoordinationMetrics{}, FollowupSpec{}, IssueConversationQueuePage{}, IssueTranscriptPage{}, IssueTranscriptBlock{}, WithdrawMessagePayload{}, WithdrawMessageResult{}, Envelope{}, APIResponse{}, Snapshot{}, RepoRegistry{}, TaskPage{}, TaskDetail{}, RunLogPage{}, LedgerEvent{}, StreamStart{}, ArtifactDetail{}, RequestView{},
		WorkItemChoice{}, UpdateWorkItemChoicePayload{}, SessionRequest{}, SessionView{}, InterruptReplyPayload{}, SubmitMessagePayload{}, AnswerDecisionPayload{}, AnswerResult{}, SetControlModePayload{}, RateTaskPayload{}, CreateActionPayload{},
		ConversationAttachment{}, DownloadURLResponse{}, ConversationImageResponse{}, ConversationSettingsView{}, SetConversationSettingsPayload{}, ConversationHandoffView{}, SetConversationHandoffSettingsPayload{}, ConversationHandoffPayload{},
		ExplainDecisionPayload{}, ArtifactPayload{}, ToolSpec{}}
}

// APIEnums lists the values of every string enum in the owner API.
func APIEnums() []any {
	return []any{
		IssueWorkStatusValues(),
		[]ArchiveState{ArchiveReady, ArchiveUnsupported, ArchiveUnavailable},
		[]IssueState{IssueActive, IssuePaused, IssueCancelling, IssueCancelled, IssueClosed},
		[]IssueAssigneeKind{IssueAssigneeOwner, IssueAssigneeAgent},
		ExecutionStateValues(), QueueWaitCodeValues(),
		SharedFactKindValues(), WorkItemStatusValues(), WorkItemUpdateStatusValues(),
		IssueStageStatusValues(), IssueFollowOnKindValues(), IssueFollowOnStatusValues(),
		IssueDeliveryProviderValues(), IssueEvidenceKindValues(), IssueClosureKindValues(), IssueTaskDispositionActionValues(),
		[]TaskRelationKind{RelationFollowUp, RelationRelated},
		[]RelationDirection{RelationOutgoing, RelationIncoming},
		[]TaskKind{TaskKindExecution, TaskKindIssueManager},
		[]TaskLifecycle{TaskDraft, TaskQueued, TaskRunning, TaskWaiting, TaskAwaitingAcceptance, TaskChangesRequired, TaskCompleted, TaskCancelled},
		[]ControlMode{ControlManaged, ControlPaused, ControlHumanControl},
		[]Rating{RatingNone, RatingP0, RatingP1, RatingP2, RatingP3},
		[]BlockReason{BlockNone, BlockWaitingResource, BlockWaitingDecision, BlockWaitingApproval, BlockAcceptanceEnvUnavailable, BlockBudgetExhausted, BlockWorkspaceUnavailable},
		[]RunObserved{RunStarting, RunRunning, RunWaiting, RunFinished, RunFailed, RunInterrupted, RunUnknown},
		[]RunDesired{DesiredRun, DesiredPaused, DesiredStopped, DesiredHeld},
		[]MessageState{MessageQueued, MessageAccepted, MessageProcessed, MessageProcessing, MessageReplied, MessageInterrupted, MessageFailed, MessageUnknown},
		[]AssistantState{AssistantStreaming, AssistantCompleted, AssistantInterrupted, AssistantFailed, AssistantUnknown},
		[]ToolStatus{ToolRunning, ToolCompleted, ToolFailed, ToolInterrupted},
		[]RunRole{RoleImplement, RoleAccept, RoleIssueManager},
		[]ActionKind{ActionImplement, ActionAccept, ActionDeliver, ActionCoordinate},
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
