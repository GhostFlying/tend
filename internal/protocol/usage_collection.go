package protocol

// UsageCollection describes local reading, independently of usage coverage and
// durable upload receipts. All timestamps and transitions use the caller's clock.
type UsageCollection struct {
	State            string `json:"state"`
	Epoch            int64  `json:"epoch"`
	Revision         int64  `json:"revision"`
	PolicyVersion    int64  `json:"policy_version"`
	Evidence         string `json:"execution_evidence"`
	WriterID         string `json:"writer_id,omitempty"`
	RequestID        string `json:"execution_request_id,omitempty"`
	StopProof        string `json:"stop_proof,omitempty"`
	FirstFailureAt   int64  `json:"first_failure_at,omitempty"`
	LastProgressAt   int64  `json:"last_progress_at,omitempty"`
	RetryDeadlineAt  int64  `json:"retry_deadline_at,omitempty"`
	NextRetryAt      int64  `json:"next_retry_at,omitempty"`
	RetryDelayMillis int64  `json:"retry_delay_ms,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
	RetirementReason string `json:"retirement_reason,omitempty"`
	RetiredAt        int64  `json:"retired_at,omitempty"`
}

const (
	CapabilityUsageCollection = "native-usage-collection-v1"
	OpRunnerUsageCollection   = "runner.usage_collection"
	OpRetireUsageCollection   = "token_usage.collection_retire"
	OpResumeUsageCollection   = "token_usage.collection_resume"
	UsageDormant              = "dormant"
	UsageActive               = "active"
	UsageSettling             = "settling"
	UsageNeedsReconcile       = "needs_reconcile"
	UsageRetired              = "retired"
	UsageNoDispatch           = "no_dispatch_proven"
	UsagePossible             = "possible"
	UsageObserved             = "observed"
	UsageUnknown              = "unknown"
	UsageRetryBudget          = int64(5 * 60 * 1000)
)

type UsageCollectionObservation struct {
	CursorFiles       int64           `json:"cursor_files"`
	CursorOffset      int64           `json:"cursor_offset"`
	Sequence          int64           `json:"sequence"`
	LocalSourceID     string          `json:"local_source_id"`
	RunID             string          `json:"run_id"`
	ThreadID          string          `json:"thread_id"`
	SessionID         string          `json:"session_id"`
	WriterStopped     bool            `json:"writer_stopped"`
	Collection        UsageCollection `json:"collection"`
	PendingRequestIDs []string        `json:"pending_request_ids,omitempty"`
	ControlRequestID  string          `json:"control_request_id,omitempty"`
	ControlStatus     string          `json:"control_status,omitempty"`
}

type UsageCollectionCommand struct {
	RequestID        string `json:"request_id"`
	SourceID         string `json:"source_id"`
	RunID            string `json:"run_id"`
	Action           string `json:"action"`
	Reason           string `json:"reason,omitempty"`
	ExpectedEpoch    int64  `json:"expected_epoch"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type UsageCollectionControl struct {
	Reason string `json:"reason,omitempty"`
}

type UsageCollectionSource struct {
	CursorFiles       int64                          `json:"cursor_files"`
	CursorOffset      int64                          `json:"cursor_offset"`
	ID                string                         `json:"id"`
	HostID            string                         `json:"host_id,omitempty"`
	RunID             string                         `json:"run_id,omitempty"`
	ConversationID    string                         `json:"conversation_id,omitempty"`
	ThreadID          string                         `json:"thread_id"`
	SessionID         string                         `json:"session_id"`
	Provider          string                         `json:"provider"`
	Coverage          string                         `json:"coverage"`
	WriterStopped     bool                           `json:"writer_stopped"`
	Collection        UsageCollection                `json:"collection"`
	ControlRevision   int64                          `json:"control_revision"`
	Commands          []UsageCollectionCommandStatus `json:"commands"`
	PendingRequestIDs []string                       `json:"pending_request_ids"`
}

type UsageCollectionCommandStatus struct {
	UsageCollectionCommand
	Status string `json:"status"`
}

type UsageCollectionPage struct {
	Sources []UsageCollectionSource `json:"sources"`
	Next    string                  `json:"next"`
	More    bool                    `json:"more"`
}

type UsageCollectionQuery struct {
	Host         string
	Run          string
	Conversation string
	Thread       string
	State        string
	Reason       string
	After        string
	Limit        int
}

func NewUsageCollection(noDispatch, stopped bool) UsageCollection {
	c := UsageCollection{State: UsageSettling, Epoch: 1, Revision: 1, PolicyVersion: 1, Evidence: UsageUnknown}
	if noDispatch {
		c.State, c.Evidence = UsageDormant, UsageNoDispatch
	}
	if stopped {
		c.State = UsageSettling
	}
	return c
}

func (c UsageCollection) Reading() bool      { return c.State == UsageActive || c.State == UsageSettling }
func (c UsageCollection) Due(now int64) bool { return c.Reading() && c.NextRetryAt <= now }

// Activate is called before dispatch or by an explicit recovery command, never
// by a generic collector wake. It preserves all earlier execution uncertainty.
func (c *UsageCollection) Activate(request, writer string) {
	c.Epoch++
	c.Revision++
	c.State, c.RequestID = UsageActive, request
	if c.Evidence == UsageNoDispatch {
		c.Evidence = UsagePossible
	}
	if writer != "" && writer != c.WriterID {
		c.WriterID, c.StopProof = writer, ""
	}
	c.RetirementReason, c.RetiredAt = "", 0
	c.clearFailure()
}

// Reopen starts a bounded read attempt without manufacturing model dispatch.
func (c *UsageCollection) Reopen(request string) {
	evidence := c.Evidence
	c.Activate(request, c.WriterID)
	c.Evidence = evidence
	c.State = UsageSettling
}

// Stopped applies only once to a newly proved writer boundary. The caller must
// fence the original writer and collection revision in its transaction.
func (c *UsageCollection) Stopped(now int64, proof string, empty bool) {
	c.Revision++
	c.StopProof = proof
	c.State = UsageSettling
	c.clearFailure()
	if empty && c.Evidence == UsageNoDispatch {
		c.retire(now, "empty")
	}
}

func (c *UsageCollection) Failed(now int64, stopped bool, code string) {
	c.FailedWithBudget(now, stopped, code, UsageRetryBudget)
}

func (c *UsageCollection) FailedWithBudget(now int64, stopped bool, code string, budget int64) {
	if budget <= 0 {
		budget = UsageRetryBudget
	}
	c.Revision++
	if c.RetryDeadlineAt == 0 {
		c.FirstFailureAt, c.RetryDeadlineAt = now, now+budget
	}
	c.ErrorCode = code
	if now >= c.RetryDeadlineAt {
		c.NextRetryAt = 0
		if stopped {
			c.retire(now, "unavailable")
		} else {
			c.State = UsageNeedsReconcile
		}
		return
	}
	c.RetryDelayMillis = min(30000, max(2000, c.RetryDelayMillis*2))
	c.NextRetryAt = min(now+c.RetryDelayMillis, c.RetryDeadlineAt)
}

// ReadSucceeded only resets the failure budget for durably committed progress.
// Empty discovery and unchanged EOF cannot renew a failing source indefinitely.
func (c *UsageCollection) ReadSucceeded(now int64, progress, caughtUp, stopped, idle bool) {
	before := *c
	if progress {
		c.LastProgressAt = now
		c.clearFailure()
	}
	if caughtUp {
		if stopped {
			c.clearFailure()
			c.retire(now, "complete")
		} else if idle {
			c.clearFailure()
			c.State = UsageDormant
		}
	}
	if *c != before {
		c.Revision++
	}
}

func (c *UsageCollection) RetireUnavailable(now int64, stopped bool) {
	if c.State == UsageRetired {
		return
	}
	c.Revision++
	c.NextRetryAt = 0
	if stopped {
		c.retire(now, "unavailable")
	} else {
		c.State = UsageNeedsReconcile
	}
}

func (c *UsageCollection) retire(now int64, reason string) {
	c.State, c.RetirementReason, c.RetiredAt, c.NextRetryAt = UsageRetired, reason, now, 0
}

func (c *UsageCollection) clearFailure() {
	c.FirstFailureAt, c.RetryDeadlineAt, c.NextRetryAt, c.RetryDelayMillis = 0, 0, 0, 0
	c.ErrorCode = ""
}
