package runnerapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestManagerAckOnlyAuthenticatesOriginalReleasedLease(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	e.createTask()
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "linux-1", protocol.ClaimPayload{WorkspaceProtocol: 1}))
	if as == nil {
		t.Fatal("no assignment")
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE runs SET role='issue_manager',observed_state='finished' WHERE run_id=?`, as.RunID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE leases SET status='released' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	original := asRun(*as)
	cases := []struct {
		name       string
		payload    protocol.ManagerNativeClaimPayload
		target     string
		credential credential
		status     int
	}{
		{"ack", protocol.ManagerNativeClaimPayload{AcknowledgeOnly: true, AcknowledgedIDs: []string{"original-close"}}, as.RunID, original, http.StatusOK},
		{"ordinary_claim", protocol.ManagerNativeClaimPayload{}, as.RunID, original, http.StatusForbidden},
		{"dispatch_with_ack", protocol.ManagerNativeClaimPayload{AcknowledgedIDs: []string{"original-close"}}, as.RunID, original, http.StatusForbidden},
		{"empty_ack", protocol.ManagerNativeClaimPayload{AcknowledgeOnly: true}, as.RunID, original, http.StatusForbidden},
		{"other_run", protocol.ManagerNativeClaimPayload{AcknowledgeOnly: true, AcknowledgedIDs: []string{"original-close"}}, e.ids.New(protocol.PrefixRun), original, http.StatusForbidden},
		{"wrong_token", protocol.ManagerNativeClaimPayload{AcknowledgeOnly: true, AcknowledgedIDs: []string{"original-close"}}, as.RunID, credential{HeaderRun, as.RunID, "not-original-token"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, result := e.call(tc.credential, e.envelope(protocol.OpManagerNativeClaim, tc.target, tc.payload))
			if status != tc.status {
				t.Fatalf("HTTP %d want %d: %+v", status, tc.status, result.Error)
			}
			if status == http.StatusOK && (result.Operation == nil || result.Operation.Status != protocol.OpSucceeded) {
				t.Fatal(result)
			}
		})
	}
}
