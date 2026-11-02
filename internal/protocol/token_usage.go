package protocol

// TokenUsageSample is a request snapshot, never a thread/turn cumulative counter.
// Source and RequestID identify the native response independently of transport
// sequence, model correction, Tend ownership, or process restart.
type TokenUsageSample struct {
	RecordID          string `json:"record_id,omitempty"`
	Provider          string `json:"provider,omitempty"`
	NativeContentHash string `json:"native_content_hash,omitempty"`
	NativeVersion     string `json:"native_version,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	Ordinal           *int64 `json:"ordinal,omitempty"`
	ModelSource       string `json:"model_source,omitempty"`
	TimeSource        string `json:"time_source,omitempty"`
	CounterSource     string `json:"counter_source,omitempty"`
	Purpose           string `json:"purpose,omitempty"`
	RootTurnID        string `json:"root_turn_id,omitempty"`
	ParentSessionID   string `json:"parent_session_id,omitempty"`
	ParentThreadID    string `json:"parent_thread_id,omitempty"`
	Source            string `json:"source"`
	RequestID         string `json:"request_id"`
	ThreadID          string `json:"thread_id"`
	TurnID            string `json:"turn_id"`
	Revision          int64  `json:"revision"`
	Settlement        string `json:"settlement"`
	Profile           string `json:"profile"`
	Model             string `json:"model"`
	OccurredAt        *int64 `json:"occurred_at"`
	Input             *int64 `json:"input"`
	CacheRead         *int64 `json:"cache_read"`
	CacheWrite        *int64 `json:"cache_write"`
	Output            *int64 `json:"output"`
	Total             *int64 `json:"total"`
}

// TokenUsageTotals reports known field subtotals and explicit field coverage.
// Null fields are never added as zero to a claim of complete measurement.
type TokenUsageTotals struct {
	EstimatedUSD      string `json:"estimated_usd,omitempty"`
	EstimatedRequests int64  `json:"estimated_requests,omitempty"`
	Requests          int64  `json:"requests"`
	Input             int64  `json:"input"`
	InputKnown        int64  `json:"input_known"`
	CacheRead         int64  `json:"cache_read"`
	CacheReadKnown    int64  `json:"cache_read_known"`
	CacheWrite        int64  `json:"cache_write"`
	CacheWriteKnown   int64  `json:"cache_write_known"`
	Output            int64  `json:"output"`
	OutputKnown       int64  `json:"output_known"`
	Total             int64  `json:"total"`
	TotalKnown        int64  `json:"total_known"`
	Final             int64  `json:"final"`
}

type TokenUsageGroup struct {
	Name string `json:"name"`
	TokenUsageTotals
}

type UsageEstimate struct {
	USD     string `json:"usd"`
	ModelID string `json:"model_id"`
}

type UsagePricing struct {
	Source   string `json:"source"`
	Currency string `json:"currency"`
	QuotedAt *int64 `json:"quoted_at"`
}

type TokenUsageRecord struct {
	Estimate   *UsageEstimate `json:"estimate,omitempty"`
	Seq        int64          `json:"seq"`
	Subject    string         `json:"subject"`
	Role       string         `json:"role"`
	ReceivedAt int64          `json:"received_at"`
	Conflict   bool           `json:"conflict"`
	TokenUsageSample
	Attribution UsageAttribution `json:"attribution"`
}

// UsageAttribution is derived from recorded execution responsibility, never the
// Issue's current assignee. One request has at most one direct Issue owner.
type UsageAttribution struct {
	ExecutionState string          `json:"execution_state,omitempty"`
	IssuePath      []UsageIssueRef `json:"issue_path"`
	TaskID         string          `json:"task_id"`
	TaskTitle      string          `json:"task_title"`
	RunID          string          `json:"run_id"`
	Agent          string          `json:"agent"`
	AgentID        string          `json:"agent_id"`
	Categories     []string        `json:"categories"`
	Role           string          `json:"role"`
}

type UsageIssueRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type UsageSession struct {
	MeteringStatus  string             `json:"metering_status,omitempty"`
	ParentSessionID string             `json:"parent_session_id"`
	Executions      []UsageExecution   `json:"executions"`
	ID              string             `json:"id"`
	Models          []string           `json:"models"`
	FirstAt         *int64             `json:"first_at"`
	LastAt          *int64             `json:"last_at"`
	Attributions    []UsageAttribution `json:"attributions"`
	Totals          TokenUsageTotals   `json:"totals"`
}

type UsageMeasurement struct {
	Model      string           `json:"model"`
	CLI        string           `json:"cli"`
	CLIVersion string           `json:"cli_version"`
	Totals     TokenUsageTotals `json:"totals"`
}

type UsageExecution struct {
	Measurements []UsageMeasurement `json:"measurements"`
	Attribution  UsageAttribution   `json:"attribution"`
	Totals       TokenUsageTotals   `json:"totals"`
}

type UsageCoverage struct {
	Sources        int64  `json:"sources"`
	Pending        int64  `json:"pending"`
	LegacyRequests int64  `json:"legacy_requests"`
	LastSyncedAt   *int64 `json:"last_synced_at"`
}

type UsageIssue struct {
	LastAt   *int64           `json:"last_at,omitempty"`
	Models   []string         `json:"models"`
	Path     []UsageIssueRef  `json:"path"`
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	ParentID string           `json:"parent_id"`
	Direct   TokenUsageTotals `json:"direct"`
	Totals   TokenUsageTotals `json:"totals"`
	Sessions []UsageSession   `json:"sessions"`
}

const CapabilityNativeUsage = "native-usage-ledger-v1"
const OpRunnerUsageUpload = "runner.usage_upload"
const OpRunnerUsageRegister = "runner.usage_register"
const OpRunnerUsageSources = "runner.usage_sources"
const OpRunnerUsagePreview = "runner.usage_preview"

// NativeUsageRegistration is a host's durable statement binding its own
// historical execution to a native source. It contains no local file paths.
type NativeUsageRegistration struct {
	RunID           string `json:"run_id"`
	ThreadID        string `json:"thread_id"`
	SessionID       string `json:"session_id"`
	Provider        string `json:"provider"`
	Purpose         string `json:"purpose,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
}

type UsageTarget struct {
	Stopped         bool   `json:"stopped,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Purpose         string `json:"purpose,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	RunID           string `json:"run_id"`
	ConversationID  string `json:"conversation_id"`
	ThreadID        string `json:"thread_id"`
}

type UsageReconcile struct {
	RunID          string             `json:"run_id,omitempty"`
	SessionID      string             `json:"session_id,omitempty"`
	Coverage       string             `json:"coverage,omitempty"`
	ConversationID string             `json:"conversation_id"`
	RootThreadID   string             `json:"root_thread_id"`
	Samples        []TokenUsageSample `json:"samples"`
}

type UsagePreview struct {
	Records      int64 `json:"records"`
	New          int64 `json:"new"`
	Existing     int64 `json:"existing"`
	LinkedLegacy int64 `json:"linked_legacy"`
	Conflicts    int64 `json:"conflicts"`
	TokenDelta   int64 `json:"token_delta"`
}

// UsageUpload binds measurements to one known run. Host authentication remains
// valid after the run lease expires; measurements cannot authorize business work.
type UsageUpload struct {
	SessionID      string             `json:"session_id,omitempty"`
	RunID          string             `json:"run_id"`
	RootThreadID   string             `json:"root_thread_id"`
	ConversationID string             `json:"conversation_id,omitempty"`
	Samples        []TokenUsageSample `json:"samples"`
	Coverage       string             `json:"coverage"`
}

// TokenUsageBucket is a stable UTC interval clipped to the selected window.
type TokenUsageBucket struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	TokenUsageTotals
}

type TokenUsageView struct {
	Pricing    UsagePricing       `json:"pricing"`
	Collection UsageCoverage      `json:"collection"`
	Sessions   []UsageSession     `json:"sessions"`
	Issues     []UsageIssue       `json:"issues"`
	Trend      []TokenUsageBucket `json:"trend"`

	Revision     int64              `json:"revision"`
	AsOf         int64              `json:"as_of"`
	Timezone     string             `json:"timezone"`
	Window       string             `json:"window"`
	Start        *int64             `json:"start"`
	End          int64              `json:"end"`
	Totals       TokenUsageTotals   `json:"totals"`
	Unattributed TokenUsageTotals   `json:"unattributed"`
	Models       []TokenUsageGroup  `json:"models"`
	Roles        []TokenUsageGroup  `json:"roles"`
	Records      []TokenUsageRecord `json:"records"`
	Next         int64              `json:"next"`
	More         bool               `json:"more"`
	Conflicts    int64              `json:"conflicts"`
	Coverage     string             `json:"coverage"`
}
