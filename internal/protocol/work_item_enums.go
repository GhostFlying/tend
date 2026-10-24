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

// WorkItemStageStatus is a lifecycle; custom stage labels belong in Title.
type WorkItemStageStatus string

const (
	WorkItemStagePlanned   WorkItemStageStatus = "planned"
	WorkItemStageActive    WorkItemStageStatus = "active"
	WorkItemStageDone      WorkItemStageStatus = "done"
	WorkItemStageCancelled WorkItemStageStatus = "cancelled"
	WorkItemStageRetired   WorkItemStageStatus = "retired"
)

func WorkItemStageStatusValues() []WorkItemStageStatus {
	return []WorkItemStageStatus{WorkItemStagePlanned, WorkItemStageActive, WorkItemStageDone, WorkItemStageCancelled, WorkItemStageRetired}
}

type WorkItemFollowOnKind string

const (
	WorkItemFollowOnCommitment WorkItemFollowOnKind = "commitment"
	WorkItemFollowOnProblem    WorkItemFollowOnKind = "problem"
	WorkItemFollowOnSuggestion WorkItemFollowOnKind = "suggestion"
)

func WorkItemFollowOnKindValues() []WorkItemFollowOnKind {
	return []WorkItemFollowOnKind{WorkItemFollowOnCommitment, WorkItemFollowOnProblem, WorkItemFollowOnSuggestion}
}

type WorkItemFollowOnStatus string

const (
	WorkItemFollowOnOpen        WorkItemFollowOnStatus = "open"
	WorkItemFollowOnResolved    WorkItemFollowOnStatus = "resolved"
	WorkItemFollowOnTransferred WorkItemFollowOnStatus = "transferred"
)

func WorkItemFollowOnStatusValues() []WorkItemFollowOnStatus {
	return []WorkItemFollowOnStatus{WorkItemFollowOnOpen, WorkItemFollowOnResolved, WorkItemFollowOnTransferred}
}

type WorkItemDeliveryProvider string

const (
	WorkItemDeliveryGitHub   WorkItemDeliveryProvider = "github"
	WorkItemDeliveryCodebase WorkItemDeliveryProvider = "codebase"
)

func WorkItemDeliveryProviderValues() []WorkItemDeliveryProvider {
	return []WorkItemDeliveryProvider{WorkItemDeliveryGitHub, WorkItemDeliveryCodebase}
}

type WorkItemEvidenceKind string

const (
	WorkItemEvidenceAcceptance    WorkItemEvidenceKind = "acceptance"
	WorkItemEvidenceDelivery      WorkItemEvidenceKind = "delivery"
	WorkItemEvidenceOwnerDecision WorkItemEvidenceKind = "owner_decision"
)

func WorkItemEvidenceKindValues() []WorkItemEvidenceKind {
	return []WorkItemEvidenceKind{WorkItemEvidenceAcceptance, WorkItemEvidenceDelivery, WorkItemEvidenceOwnerDecision}
}

type WorkItemClosureKind string

const (
	WorkItemClosureCompleted  WorkItemClosureKind = "completed"
	WorkItemClosureDeclined   WorkItemClosureKind = "declined"
	WorkItemClosureHandedOff  WorkItemClosureKind = "handed_off"
	WorkItemClosureSuperseded WorkItemClosureKind = "superseded"
)

func WorkItemClosureKindValues() []WorkItemClosureKind {
	return []WorkItemClosureKind{WorkItemClosureCompleted, WorkItemClosureDeclined, WorkItemClosureHandedOff, WorkItemClosureSuperseded}
}

type WorkItemTaskDispositionAction string

const (
	WorkItemTaskContinueExisting WorkItemTaskDispositionAction = "continue_existing"
	WorkItemTaskUpdated          WorkItemTaskDispositionAction = "updated"
	WorkItemTaskStopped          WorkItemTaskDispositionAction = "stopped"
)

func WorkItemTaskDispositionActionValues() []WorkItemTaskDispositionAction {
	return []WorkItemTaskDispositionAction{WorkItemTaskContinueExisting, WorkItemTaskUpdated, WorkItemTaskStopped}
}
