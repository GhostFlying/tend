package protocol

// APITypes are the root types of the owner API's JSON Schema, from which
// the web client's types are generated (D22). Types they refer to are
// included through them.
func APITypes() []any {
	return []any{Envelope{}, Operation{}, TaskSummary{}, TaskDetail{}, ArtifactDetail{}, RequestView{}, SessionRequest{}, SessionView{}}
}

// APIEnums lists the values of every string enum in the owner API.
func APIEnums() []any {
	return []any{
		[]TaskLifecycle{TaskDraft, TaskQueued, TaskRunning, TaskAwaitingAcceptance, TaskChangesRequired, TaskCompleted, TaskCancelled},
		[]ControlMode{ControlManaged, ControlPaused, ControlHumanControl},
		[]BlockReason{BlockNone, BlockWaitingResource, BlockWaitingDecision, BlockWaitingApproval, BlockAcceptanceEnvUnavailable, BlockBudgetExhausted},
		[]RunObserved{RunStarting, RunRunning, RunWaiting, RunFinished, RunFailed, RunInterrupted, RunUnknown},
		[]RunDesired{DesiredRun, DesiredPaused, DesiredStopped},
		[]MessageState{MessageQueued, MessageProcessing, MessageReplied, MessageInterrupted, MessageFailed, MessageUnknown},
		[]RunRole{RoleImplement, RoleAccept},
		[]ActionKind{ActionImplement, ActionAccept, ActionDeliver},
		[]ActionStatus{ActionPending, ActionReady, ActionDispatched, ActionDone, ActionCancelled, ActionSuperseded},
		[]Verdict{VerdictPassed, VerdictChangesRequired, VerdictBlocked, VerdictInconclusive},
		[]Code{CodeInvalidRequest, CodeMissingRequestID, CodeConflictPayload, CodeStaleRevision, CodeUnauthenticated, CodeForbidden, CodeNotFound,
			CodeInvalidTransition, CodeDuplicateEffect, CodeUnknownOutcome, CodeInternal},
		[]OpStatus{OpSucceeded, OpFailed, OpAccepted, OpUnknown},
	}
}
