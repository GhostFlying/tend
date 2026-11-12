package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueOwnerProgressCLIExportsOriginalFilesWithoutUpgrade(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	want := protocol.UpdateIssueDeliveryPayload{
		Action: "submit_result", TaskID: "original-owner-task", RunID: "run_01J00000000000000000000000",
		Result: &protocol.ArtifactPayload{
			Report: &protocol.ResultReport{Body: "The current candidate passed local checks; deployment has not started."},
			Files:  []protocol.ArtifactFile{{FileID: "original-check-file", Name: "checks.log", Size: 123, SHA256: strings.Repeat("a", 64)}},
		},
		IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: 3, Reason: "Register the current Run's original check report"},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if code := e.main("issue", "update-delivery", "original-issue", "--revision", "7", "--epoch", "3", "--data", string(raw), "--dry-run", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var env protocol.Envelope
	var got protocol.UpdateIssueDeliveryPayload
	if json.Unmarshal(e.out.Bytes(), &env) != nil || json.Unmarshal(env.Payload, &got) != nil || !reflect.DeepEqual(got, want) || env.Operation != "issues.update_delivery" || env.Target.ID != "original-issue" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 {
		t.Fatal("the CLI changed the real producer, original files or control revision", env, got)
	}
	if got.UpgradeID != "" || len(got.Result.Report.Evidence) != 0 || len(f.requests) != 0 {
		t.Fatal("predeployment recording invented an upgrade or performed a write", got)
	}
}
