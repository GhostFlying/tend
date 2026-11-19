package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWriteCommandHelpDistinguishesReadsAndWrites(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{"", []string{"issue dependencies  Write (alias for issue set-dependencies)", "host set-maintenance  Write runner admission", "issue show  Read Issue details"}},
		{"issue", []string{"issue set-dependencies", "legacy issue dependencies command also writes", "issue refresh-delivery"}},
		{"issue show", []string{"Read one Issue", "depends_on", `tend issue show "$ISSUE_ID" --json`}},
		{"issue set-dependencies", []string{"replace the entire", `{"depends_on":[]}`, "null depends_on is rejected", "--dry-run", "--request-id", "--revision", "--epoch"}},
		{"issue dependencies", []string{"Compatibility alias", "issue set-dependencies", "replace the entire", "issue show"}},
		{"issue reparent", []string{"Write: change the parent", `{"parent_issue_id":""}`, "expected_parent_revision", "issue show"}},
		{"issue update-progress", []string{"Write: replace", `{"summary":"","next_step":""}`, "conflict_revision", "issue show", "same observation", "Copy both base_sha and target_sha", "only when the observation omits it"}},
		{"issue deliveries", []string{"Read recorded delivery observations", "delivery_id"}},
		{"issue refresh-delivery", []string{"Write: queue", "delivery_id", "issue deliveries", "--dry-run", "--request-id"}},
		{"host", []string{"host set-maintenance", "legacy host maintenance command also writes"}},
		{"host set-maintenance", []string{"Write: set admission", "host show", "--dry-run", "--revision", "--request-id", "maintenance.mode and maintenance.phase", "initial revision 0", "runner's last report", "readback is inconclusive"}},
		{"host maintenance", []string{"Compatibility alias", "host set-maintenance", "Write: set admission", "maintenance.mode and maintenance.phase"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			env := newTestEnv(t, "")
			args := strings.Fields(tc.command)
			if code := env.main(append(args, "--help")...); code != exitOK || env.errOut.Len() != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, env.out, env.errOut)
			}
			for _, want := range tc.want {
				if !strings.Contains(env.out.String(), want) {
					t.Errorf("missing %q in %s", want, env.out)
				}
			}
			if strings.HasPrefix(tc.command, "host") && strings.Contains(env.out.String(), "control_mode") {
				t.Errorf("help mentions a field absent from host show: %s", env.out)
			}
			direct := env.out.String()
			env.out.Reset()
			if code := env.main(append([]string{"help"}, args...)...); code != exitOK || env.out.String() != direct {
				t.Fatalf("help dispatch differs: exit=%d stdout=%s stderr=%s", code, env.out, env.errOut)
			}
		})
	}
}

func TestHostSetMaintenanceAliasUsesSameEnvelope(t *testing.T) {
	server := newFakeServer(t)
	env := newTestEnv(t, server.srv.URL)
	request := env.IDs.New(protocol.PrefixRequest)
	flags := []string{"test-host", "--revision", "0", "--request-id", request, "--data", `{"mode":"maintenance"}`, "--dry-run"}
	if code := env.main(append([]string{"host", "set-maintenance"}, flags...)...); code != exitOK {
		t.Fatal(code, env.errOut.String())
	}
	envelope := bytes.Clone(env.out.Bytes())
	env.out.Reset()
	if code := env.main(append([]string{"host", "maintenance"}, flags...)...); code != exitOK {
		t.Fatal(code, env.errOut.String())
	}
	if !bytes.Equal(envelope, env.out.Bytes()) || len(server.requests) != 0 {
		t.Fatalf("dry-run changed the Envelope or sent HTTP:\n%s\n%s", envelope, env.out)
	}
}
