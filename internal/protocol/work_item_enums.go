package protocol

// SharedFactKind classifies attributable context without prescribing a design.
type SharedFactKind string

const (
	SharedFactGoal           SharedFactKind = "goal"
	SharedFactObservation    SharedFactKind = "observation"
	SharedFactUserConstraint SharedFactKind = "user_constraint"
	SharedFactDecision       SharedFactKind = "decision"
	SharedFactInput          SharedFactKind = "input"
	SharedFactFact           SharedFactKind = "fact"
)

func SharedFactKindValues() []SharedFactKind {
	return []SharedFactKind{SharedFactGoal, SharedFactObservation, SharedFactUserConstraint, SharedFactDecision, SharedFactInput, SharedFactFact}
}

// WorkItemStatus describes responsibility, independently of stage titles.
type WorkItemStatus string

const (
	WorkItemOpen    WorkItemStatus = "open"
	WorkItemSnoozed WorkItemStatus = "snoozed"
	WorkItemClosed  WorkItemStatus = "closed"
)

func WorkItemStatusValues() []WorkItemStatus {
	return []WorkItemStatus{WorkItemOpen, WorkItemSnoozed, WorkItemClosed}
}

// WorkItemUpdateStatus excludes closure, which has its own evidence-bearing
// operation. Empty preserves the current status, including when sent explicitly.
type WorkItemUpdateStatus string

const (
	WorkItemUpdateUnchanged WorkItemUpdateStatus = ""
	WorkItemUpdateOpen      WorkItemUpdateStatus = "open"
	WorkItemUpdateSnoozed   WorkItemUpdateStatus = "snoozed"
)

func WorkItemUpdateStatusValues() []WorkItemUpdateStatus {
	return []WorkItemUpdateStatus{WorkItemUpdateUnchanged, WorkItemUpdateOpen, WorkItemUpdateSnoozed}
}

// IssueStageStatus is a lifecycle; custom stage labels belong in Title.
type IssueStageStatus string

const (
	WorkItemStagePlanned   IssueStageStatus = "planned"
	WorkItemStageActive    IssueStageStatus = "active"
	WorkItemStageDone      IssueStageStatus = "done"
	WorkItemStageCancelled IssueStageStatus = "cancelled"
	WorkItemStageRetired   IssueStageStatus = "retired"
)

func IssueStageStatusValues() []IssueStageStatus {
	return []IssueStageStatus{WorkItemStagePlanned, WorkItemStageActive, WorkItemStageDone, WorkItemStageCancelled, WorkItemStageRetired}
}

type IssueFollowOnKind string

const (
	WorkItemFollowOnCommitment IssueFollowOnKind = "commitment"
	WorkItemFollowOnProblem    IssueFollowOnKind = "problem"
	WorkItemFollowOnSuggestion IssueFollowOnKind = "suggestion"
)

func IssueFollowOnKindValues() []IssueFollowOnKind {
	return []IssueFollowOnKind{WorkItemFollowOnCommitment, WorkItemFollowOnProblem, WorkItemFollowOnSuggestion}
}

type IssueFollowOnStatus string

const (
	WorkItemFollowOnOpen        IssueFollowOnStatus = "open"
	WorkItemFollowOnResolved    IssueFollowOnStatus = "resolved"
	WorkItemFollowOnTransferred IssueFollowOnStatus = "transferred"
)

func IssueFollowOnStatusValues() []IssueFollowOnStatus {
	return []IssueFollowOnStatus{WorkItemFollowOnOpen, WorkItemFollowOnResolved, WorkItemFollowOnTransferred}
}

type IssueDeliveryProvider string

const (
	WorkItemDeliveryGitHub   IssueDeliveryProvider = "github"
	WorkItemDeliveryCodebase IssueDeliveryProvider = "codebase"
)

func IssueDeliveryProviderValues() []IssueDeliveryProvider {
	return []IssueDeliveryProvider{WorkItemDeliveryGitHub, WorkItemDeliveryCodebase}
}

type IssueEvidenceKind string

const (
	WorkItemEvidenceAcceptance    IssueEvidenceKind = "acceptance"
	WorkItemEvidenceUpgrade       IssueEvidenceKind = "upgrade"
	WorkItemEvidenceDelivery      IssueEvidenceKind = "delivery"
	WorkItemEvidenceOwnerDecision IssueEvidenceKind = "owner_decision"
)

func IssueEvidenceKindValues() []IssueEvidenceKind {
	return []IssueEvidenceKind{WorkItemEvidenceAcceptance, WorkItemEvidenceDelivery, WorkItemEvidenceOwnerDecision, WorkItemEvidenceUpgrade}
}

type IssueClosureKind string

const (
	WorkItemClosureCompleted  IssueClosureKind = "completed"
	WorkItemClosureDeclined   IssueClosureKind = "declined"
	WorkItemClosureHandedOff  IssueClosureKind = "handed_off"
	WorkItemClosureSuperseded IssueClosureKind = "superseded"
)

func IssueClosureKindValues() []IssueClosureKind {
	return []IssueClosureKind{WorkItemClosureCompleted, WorkItemClosureDeclined, WorkItemClosureHandedOff, WorkItemClosureSuperseded}
}

type IssueTaskDispositionAction string

const (
	WorkItemTaskContinueExisting IssueTaskDispositionAction = "continue_existing"
	WorkItemTaskUpdated          IssueTaskDispositionAction = "updated"
	WorkItemTaskStopped          IssueTaskDispositionAction = "stopped"
)

func IssueTaskDispositionActionValues() []IssueTaskDispositionAction {
	return []IssueTaskDispositionAction{WorkItemTaskContinueExisting, WorkItemTaskUpdated, WorkItemTaskStopped}
}
