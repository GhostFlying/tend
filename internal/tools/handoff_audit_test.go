package tools

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHandoffAuditToolIsMainOnlyAndReadOnly(t *testing.T) {
	e := newEnv(t)
	args := handoffAuditArgs{Instance: "original", After: 17, Limit: 2}
	answer := e.call("conversation_handoff_audit", args, "audit")
	var page protocol.HandoffAuditPage
	if !answer.OK || json.Unmarshal(answer.Result, &page) != nil || page.Instance != "original" || page.NextAfter != 17 || page.HistoryComplete {
		t.Fatal(answer, page)
	}
	raw, _ := json.Marshal(args)
	for _, principal := range []protocol.Principal{{Kind: protocol.PrincipalBackgroundAgent, Subject: "issue-manager"}, {Kind: protocol.PrincipalRun, Subject: "run", Role: protocol.RoleIssueManager}} {
		if _, err := e.svc.handoffAudit(t.Context(), Call{Principal: principal, Arguments: raw}); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("Issue executor admitted to global audit", principal, err)
		}
	}
}
