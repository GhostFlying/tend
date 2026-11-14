package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueProgressCLIHonorsUnicodeContractBeforeSubmission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count, exit int
	}{
		{"boundary", 500, exitOK},
		{"overflow", 501, exitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			te := newTestEnv(t, f.srv.URL)
			body, err := json.Marshal(protocol.UpdateIssueProgressPayload{Summary: strings.Repeat("界", tc.count), NextStep: strings.Repeat("🙂", 240), ExpectedAssignmentEpoch: 1})
			if err != nil {
				t.Fatal(err)
			}
			if code := te.main("issue", "update-progress", "wi_target", "--data", string(body), "--revision", "3", "--epoch", "1", "--dry-run"); code != tc.exit {
				t.Fatal(code, te.errOut.String())
			}
			if len(f.requests) != 0 {
				t.Fatal("dry-run or input rejection performed network IO")
			}
			if tc.exit == exitUsage && (!strings.Contains(te.errOut.String(), "summary") || !strings.Contains(te.errOut.String(), "500")) {
				t.Fatal("CLI omitted exact field/limit", te.errOut.String())
			}
		})
	}
}
