package e2e

import "testing"

// This model-free fault injects only an uncertain close receipt. The normal
// desired-run executor must leave that retry stage without external cancel;
// real App then authenticates physical stop, releases the original lease,
// accepts the unchanged late close and derives its original receipt reader.
func TestFault_NormalManagerReceiptDeadlineHTTPRecovery(t *testing.T) {
	closedRepairToolFacts(t, closedManagerRecoveryOptions{normal: true})
}
