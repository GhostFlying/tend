package protocol

// RunEventPart transports the original RPC payload. Only the two event
// operations accept parts; a part never represents an applied business action.
type RunEventPart struct {
	Operation string `json:"operation"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Offset    int64  `json:"offset"`
	Data      []byte `json:"data"`
}

const CapabilityEventSync = "run-event-sync-v1"

const RunEventPartBytes = 256 << 10

type RunEventPartReceipt struct {
	SHA256 string `json:"sha256"`
	Next   int64  `json:"next"`
	Stored bool   `json:"stored"`
}
