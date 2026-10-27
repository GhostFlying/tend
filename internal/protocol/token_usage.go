package protocol

// TokenUsageSample is a request snapshot, never a thread/turn cumulative counter.
// Source and RequestID identify the native response independently of transport
// sequence, model correction, Tend ownership, or process restart.
type TokenUsageSample struct {
	ParentThreadID string `json:"parent_thread_id,omitempty"`
	Source         string `json:"source"`
	RequestID      string `json:"request_id"`
	ThreadID       string `json:"thread_id"`
	TurnID         string `json:"turn_id"`
	Revision       int64  `json:"revision"`
	Settlement     string `json:"settlement"`
	Profile        string `json:"profile"`
	Model          string `json:"model"`
	OccurredAt     *int64 `json:"occurred_at"`
	Input          *int64 `json:"input"`
	CacheRead      *int64 `json:"cache_read"`
	CacheWrite     *int64 `json:"cache_write"`
	Output         *int64 `json:"output"`
	Total          *int64 `json:"total"`
}

// TokenUsageTotals reports known field subtotals and explicit field coverage.
// Null fields are never added as zero to a claim of complete measurement.
type TokenUsageTotals struct {
	Requests        int64 `json:"requests"`
	Input           int64 `json:"input"`
	InputKnown      int64 `json:"input_known"`
	CacheRead       int64 `json:"cache_read"`
	CacheReadKnown  int64 `json:"cache_read_known"`
	CacheWrite      int64 `json:"cache_write"`
	CacheWriteKnown int64 `json:"cache_write_known"`
	Output          int64 `json:"output"`
	OutputKnown     int64 `json:"output_known"`
	Total           int64 `json:"total"`
	TotalKnown      int64 `json:"total_known"`
	Final           int64 `json:"final"`
}

type TokenUsageGroup struct {
	Name string `json:"name"`
	TokenUsageTotals
}

type TokenUsageRecord struct {
	Seq        int64  `json:"seq"`
	Subject    string `json:"subject"`
	Role       string `json:"role"`
	ReceivedAt int64  `json:"received_at"`
	Conflict   bool   `json:"conflict"`
	TokenUsageSample
}

// TokenUsageBucket is a stable UTC interval clipped to the selected window.
type TokenUsageBucket struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	TokenUsageTotals
}

type TokenUsageView struct {
	Trend []TokenUsageBucket `json:"trend"`

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
