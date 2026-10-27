package protocol

// RunRolloutPage is a bounded durable window. Cursors are server log sequence
// numbers and survive reconnects; a page never changes formal execution state.
type RunRolloutPage struct {
	RunID      string       `json:"run_id"`
	Observed   RunObserved  `json:"observed"`
	Logs       []RunLogView `json:"logs"`
	Before     int64        `json:"before"`
	After      int64        `json:"after"`
	Watermark  int64        `json:"watermark"`
	MoreBefore bool         `json:"more_before"`
	MoreAfter  bool         `json:"more_after"`
}
