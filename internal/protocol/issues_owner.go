package protocol

// IssueSourceReference is material cited by the authenticated owner request.
// It does not transfer the message's author or execution responsibility.
type IssueSourceReference struct {
	SourceMessageID        string `json:"source_message_id,omitempty"`
	ExpectedSourceRevision *int64 `json:"expected_source_revision,omitempty"`
}

type CreateIssuePayload struct {
	IssueSourceReference
	Plan                   WorkItemPlan   `json:"plan"`
	Assignee               *IssueAssignee `json:"assignee,omitempty"`
	ParentIssueID          string         `json:"parent_issue_id,omitempty"`
	ExpectedParentRevision *int64         `json:"expected_parent_revision,omitempty"`
	DependsOn              []string       `json:"depends_on,omitempty"`
}

type UpdateIssuePayload struct {
	IssueSourceReference
	Plan                    WorkItemPlan              `json:"plan"`
	ExpectedAssignmentEpoch int64                     `json:"expected_assignment_epoch"`
	TaskDispositions        []WorkItemTaskDisposition `json:"task_dispositions,omitempty"`
}

type IssueControlPayload struct {
	IssueSourceReference
	ExpectedAssignmentEpoch int64  `json:"expected_assignment_epoch"`
	Reason                  string `json:"reason,omitempty"`
}

type IssueFollowOnDisposition struct {
	FollowOnID string             `json:"follow_on_id"`
	Reason     string             `json:"reason"`
	Evidence   []WorkItemEvidence `json:"evidence"`
}

type IssueSourceDisposition struct {
	MessageID           string `json:"message_id"`
	ContentRevision     int64  `json:"content_revision"`
	AssociationRevision int64  `json:"association_revision"`
	Reason              string `json:"reason"`
}

type IssueEndpointPayload struct {
	IssueControlPayload
	Closure                 *WorkItemClosure                  `json:"closure,omitempty"`
	FollowOnDispositions    []IssueFollowOnDisposition        `json:"follow_on_dispositions,omitempty"`
	SourceDispositions      []IssueSourceDisposition          `json:"source_dispositions,omitempty"`
	ExpectedContextRevision *int64                            `json:"expected_context_revision,omitempty"`
	CompletionReviews       []CompletionReviewAcknowledgement `json:"completion_reviews,omitempty"`
}

type ReparentIssuePayload struct {
	IssueControlPayload
	ParentIssueID          string `json:"parent_issue_id"`
	ExpectedParentRevision *int64 `json:"expected_parent_revision,omitempty"`
}

type SetIssueDependenciesPayload struct {
	IssueControlPayload
	DependsOn []string `json:"depends_on"`
}

type ReviewIssueSourcesPayload struct {
	IssueControlPayload
	SourceDispositions      []IssueSourceDisposition `json:"source_dispositions"`
	ExpectedContextRevision *int64                   `json:"expected_context_revision"`
}

type ReviewIssueCompletionsPayload struct {
	IssueControlPayload
	Reviews []CompletionReviewAcknowledgement `json:"reviews"`
}

// OwnerTaskSpec is an owner input contract, not the server's frozen Spec. The
// server supplies shared context and validates these fields through domain.Spec.
// Existing runner and stored-spec representations remain unchanged.
type OwnerTaskSpec struct {
	AcceptanceExecution   *ExecutionProfile     `json:"acceptance_execution,omitempty"`
	Observation           *ObservationScope     `json:"observation,omitempty"`
	Goal                  string                `json:"goal"`
	Scope                 string                `json:"scope,omitempty"`
	Constraints           []string              `json:"constraints,omitempty"`
	Acceptance            []string              `json:"acceptance"`
	Delivery              string                `json:"delivery,omitempty"`
	Deliverables          []string              `json:"deliverables,omitempty"`
	Inputs                []OwnerTaskInput      `json:"inputs,omitempty"`
	ImplementationContext string                `json:"implementation_context,omitempty"`
	MaxImplementAttempts  int64                 `json:"max_implement_attempts,omitempty"`
	Workspace             OwnerTaskWorkspace    `json:"workspace"`
	Requirements          OwnerTaskRequirements `json:"requirements,omitzero"`
}

type OwnerTaskWorkspace struct {
	Mode                   string           `json:"mode,omitempty"`
	SourceTaskID           string           `json:"source_task_id,omitempty"`
	IncludeExistingChanges bool             `json:"include_existing_changes,omitempty"`
	Template               string           `json:"template,omitempty"`
	Mounts                 []OwnerTaskMount `json:"mounts"`
	Profile                string           `json:"profile,omitempty"`
}

type OwnerTaskMount struct {
	Repo    string        `json:"repo"`
	Path    string        `json:"path,omitempty"`
	Base    OwnerTaskBase `json:"base,omitzero"`
	Profile string        `json:"profile,omitempty"`
}

type OwnerTaskBase struct {
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type OwnerTaskRequirements struct {
	HostID string   `json:"host_id,omitempty"`
	OS     string   `json:"os,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type CreateIssueTaskPayload struct {
	IssueSourceReference
	ExpectedAssignmentEpoch int64          `json:"expected_assignment_epoch"`
	Title                   string         `json:"title"`
	Spec                    OwnerTaskSpec  `json:"spec"`
	Rating                  Rating         `json:"rating,omitempty"`
	WorkspaceName           string         `json:"workspace_name,omitempty"`
	StageIDs                []string       `json:"stage_ids,omitempty"`
	Relations               []TaskRelation `json:"relations,omitempty"`
	UseDefaultWorkspace     bool           `json:"use_default_workspace,omitempty"`
}

// UpdateIssueTaskPayload revises an existing independent task agreement under
// its task control revision and the primary issue's current assignment epoch.
type UpdateIssueTaskPayload struct {
	ExpectedAssignmentEpoch int64           `json:"expected_assignment_epoch"`
	Spec                    *OwnerTaskSpec  `json:"spec,omitempty"`
	Rating                  *Rating         `json:"rating,omitempty"`
	Relations               *[]TaskRelation `json:"relations,omitempty"`
}

// OwnerTaskInput selects an original; all identity metadata is server-frozen.
type OwnerTaskInput struct {
	FileID string `json:"file_id"`
}
