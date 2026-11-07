package protocol

// DeliveryObserverStatus describes configuration, not successful provider access.
// Executables records startup availability without exposing local paths or keys.
type DeliveryObserverStatus struct {
	Enabled       bool            `json:"enabled"`
	Providers     []string        `json:"providers"`
	Executables   map[string]bool `json:"executables"`
	TimeoutMillis int64           `json:"timeout_ms"`
}
