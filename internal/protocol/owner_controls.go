package protocol

// HostMaintenancePayload controls admission of new runner allocations.
type HostMaintenancePayload struct {
	Mode string `json:"mode"`
}

// UpgradeControlPayload changes an existing authorized release batch.
type UpgradeControlPayload struct {
	Action string `json:"action"`
}
