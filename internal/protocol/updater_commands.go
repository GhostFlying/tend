package protocol

// UpdaterHandoffActions is the local executable surface shared by the installer
// dispatcher and CLI capability catalog. It is not a runner wire operation.
func UpdaterHandoffActions() []string { return []string{"plan", "apply", "status"} }

func ValidUpdaterHandoffAction(action string) bool {
	for _, value := range UpdaterHandoffActions() {
		if value == action {
			return true
		}
	}
	return false
}
