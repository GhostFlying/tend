package protocol

// IssueDeliveryProgress binds public progress to an observed PR candidate.
// ConflictRevision identifies the observed conflict episode for an explicit owner action.
type IssueDeliveryProgress struct {
	DeliveryID       string                `json:"delivery_id"`
	Provider         IssueDeliveryProvider `json:"provider"`
	Repository       string                `json:"repository"`
	Number           int64                 `json:"number"`
	HeadSHA          string                `json:"head_sha"`
	BaseSHA          string                `json:"base_sha,omitempty"`
	TargetSHA        string                `json:"target_sha,omitempty"`
	ConflictRevision int64                 `json:"conflict_revision,omitempty"`
	ConflictAction   string                `json:"conflict_action,omitempty"`
}

func (p IssueDeliveryProgress) Matches(d IssueDelivery, o DeliveryObservation) bool {
	return p.DeliveryID == d.DeliveryID && p.Provider == d.Provider && p.Repository == d.Repository && p.Number == d.Number &&
		p.HeadSHA == o.HeadSHA && p.BaseSHA == o.BaseSHA && p.TargetSHA == o.TargetSHA
}

// IssueProgress is a manager's assessment, separate from execution facts.
type IssueProgress struct {
	Delivery         *IssueDeliveryProgress `json:"delivery,omitempty"`
	Historical       bool                   `json:"historical,omitempty"`
	HistoricalReason string                 `json:"historical_reason,omitempty"`
	Summary          string                 `json:"summary"`
	NextStep         string                 `json:"next_step"`
	IssueRevision    int64                  `json:"issue_revision"`
	UpdatedAt        int64                  `json:"updated_at"`
	UpdatedBy        IssueAssignee          `json:"updated_by"`
}

type UpdateIssueProgressPayload struct {
	Delivery                *IssueDeliveryProgress `json:"delivery,omitempty"`
	ExpectedAssignmentEpoch int64                  `json:"expected_assignment_epoch"`
	Summary                 string                 `json:"summary"`
	NextStep                string                 `json:"next_step"`
}

// IssueConversationBinding is read-only; reading it never opens a conversation.
type IssueConversationBinding struct {
	ConversationID string `json:"conversation_id,omitempty"`
	CanSend        bool   `json:"can_send"`
	Reason         string `json:"reason,omitempty"`
}

type IssueAncestor struct {
	ShortCode string     `json:"short_code,omitempty"`
	IssueID   string     `json:"issue_id"`
	Title     string     `json:"title"`
	State     IssueState `json:"state"`
}

// IssueActivityEntry keeps stable ledger order while its transcript streams.
type IssueActivityEntry struct {
	Delivery   *IssueDeliveryView    `json:"delivery,omitempty"`
	Progress   *IssueProgress        `json:"progress,omitempty"`
	ID         string                `json:"id"`
	Position   int64                 `json:"position"`
	Kind       string                `json:"kind"`
	CreatedAt  int64                 `json:"created_at"`
	Author     string                `json:"author"`
	Title      string                `json:"title,omitempty"`
	IssueID    string                `json:"issue_id,omitempty"`
	TaskID     string                `json:"task_id,omitempty"`
	ArtifactID string                `json:"artifact_id,omitempty"`
	Block      *IssueTranscriptBlock `json:"block,omitempty"`
	Admission  bool                  `json:"admission,omitempty"`
}

type IssueActivityPage struct {
	Items      []IssueActivityEntry `json:"items"`
	More       bool                 `json:"more"`
	NextBefore int64                `json:"next_before,omitempty"`
	EventSeq   int64                `json:"event_seq"`
	Readable   bool                 `json:"readable,omitempty"`
	Removed    []string             `json:"removed,omitempty"`
}

// IssueResult identifies one immutable artifact and only its matching verdicts.
type IssueResult struct {
	ArtifactDetail
	Title       string `json:"title"`
	RunID       string `json:"run_id"`
	SpecVersion int64  `json:"spec_version"`
	Generation  int64  `json:"generation"`
	CreatedAt   int64  `json:"created_at"`
}

type IssueResultPage struct {
	Items []IssueResult `json:"items"`
	More  bool          `json:"more"`
	Next  string        `json:"next,omitempty"`
}

// IssueEventNotification describes original notice evidence for one activity event.
type IssueEventNotification struct {
	Position     int64  `json:"position"`
	BatchID      string `json:"batch_id"`
	MessageID    string `json:"message_id"`
	BatchStatus  string `json:"batch_status"`
	Delivery     string `json:"delivery"`
	Source       string `json:"source"`
	StartedAt    int64  `json:"started_at,omitempty"`
	NativeStatus string `json:"native_status,omitempty"`
	Settlement   string `json:"settlement"`
	Reason       string `json:"reason,omitempty"`
}

type IssueEventNotificationPage struct {
	EventID         string                   `json:"event_id"`
	CoalescingUntil int64                    `json:"coalescing_until,omitempty"`
	Notifications   []IssueEventNotification `json:"notifications"`
	Next            int64                    `json:"next,omitempty"`
}
