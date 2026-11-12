package e2e

import "testing"

func TestEndToEndMigratedManagerDisconnectHandsWaitToHistory(t *testing.T) {
	runRunnerManager(t, false, false, false, "external_wait_migrated")
}
