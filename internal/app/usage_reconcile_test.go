package app

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestCanonicalUsageTargetsCompletesRepeatedNativeEvidence(t *testing.T) {
	var discovered []protocol.UsageTarget
	for i := range 147 {
		run, thread := fmt.Sprintf("run-%d", i), fmt.Sprintf("thread-%d", i)
		discovered = append(discovered,
			protocol.UsageTarget{RunID: run, ThreadID: thread, Provider: "codex", Purpose: "issue_owner_native"},
			protocol.UsageTarget{RunID: run, ThreadID: thread, ConversationID: "issue:original"},
			protocol.UsageTarget{RunID: run, ThreadID: thread, Provider: "native", ParentSessionID: "parent"})
	}
	targets, err := canonicalUsageTargets(discovered)
	if err != nil || len(targets) != 147 {
		t.Fatal(len(targets), err)
	}
	for _, target := range targets {
		if target.ConversationID != "issue:original" || target.Provider != "codex" || target.Purpose != "issue_owner_native" || target.ParentSessionID != "parent" {
			t.Fatal("persistent evidence was discarded", target)
		}
	}
}

func TestCanonicalUsageTargetsRejectsActualMetadataConflicts(t *testing.T) {
	for _, field := range []string{"conversation", "provider", "purpose", "parent"} {
		t.Run(field, func(t *testing.T) {
			first := protocol.UsageTarget{RunID: "original", ThreadID: "thread", ConversationID: "issue:one", Provider: "codex", Purpose: "issue_owner_native", ParentSessionID: "parent"}
			second := first
			switch field {
			case "conversation":
				second.ConversationID = "issue:other"
			case "provider":
				second.Provider = "traex"
			case "purpose":
				second.Purpose = "internal_summary"
			case "parent":
				second.ParentSessionID = "other-parent"
			}
			if _, err := canonicalUsageTargets([]protocol.UsageTarget{first, second}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
				t.Fatal("contradictory native ownership was selected arbitrarily", err)
			}
		})
	}
	// Distinct hosted conversation identities remain distinct sources.
	targets, err := canonicalUsageTargets([]protocol.UsageTarget{{ConversationID: "one", ThreadID: "thread"}, {ConversationID: "two", ThreadID: "thread"}})
	if err != nil || len(targets) != 2 {
		t.Fatal(targets, err)
	}
}

func TestUsageTargetsUsesOriginalCoordinatorBindingAndHost(t *testing.T) {
	h, assignment := messageRun(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	conversation := "issue:" + issue.IssueID
	execution := contextProjectionTurn(t, h, "native-root", conversation, issue.IssueID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE runner_coordination_executions SET private_snapshot=? WHERE execution_id=?`, fmt.Sprintf(`{"runner_run_id":%q}`, assignment.RunID), execution)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The source and log intentionally omit the conversation, as native
	// registration and retained child evidence do in the real failure.
	registration := protocol.NativeUsageRegistration{RunID: assignment.RunID, ThreadID: "native-root", SessionID: "session", Provider: "codex", Purpose: "issue_owner_native"}
	h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: "linux-1"}, registration, nil), RegisterNativeUsage{})
	registration.ThreadID = "child-root"
	h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: "linux-1"}, registration, nil), RegisterNativeUsage{})
	targets, err := h.app.UsageTargets(t.Context(), runner("linux-1"))
	if err != nil || len(targets) != 2 {
		t.Fatal(targets, err)
	}
	for _, target := range targets {
		if target.RunID != assignment.RunID || target.ConversationID != conversation || target.Provider != "codex" || target.Purpose != "issue_owner_native" {
			t.Fatal("real coordinator identity was not reconstructed", target)
		}
	}
	if foreign, err := h.app.UsageTargets(t.Context(), runner("other-host")); err != nil || len(foreign) != 0 {
		t.Fatal("host source inventory crossed host ownership", foreign, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE token_usage_sources SET conversation_id='issue:conflicting' WHERE run_id=?`, assignment.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.UsageTargets(t.Context(), runner("linux-1")); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("real source/coordinator conflict was hidden", err)
	}
}

func TestUsageTargetsRejectsCatalogHostDifferentFromRealRun(t *testing.T) {
	h, assignment := messageRun(t)
	registration := protocol.NativeUsageRegistration{RunID: assignment.RunID, ThreadID: "native-root", SessionID: "session", Provider: "codex"}
	h.exec(runner("linux-1"), h.env(protocol.OpRunnerUsageRegister, protocol.Target{Type: "host", ID: "linux-1"}, registration, nil), RegisterNativeUsage{})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE token_usage_sources SET host_id='other-host' WHERE run_id=?`, assignment.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if targets, err := h.app.UsageTargets(t.Context(), runner("other-host")); err != nil || len(targets) != 0 {
		body, _ := json.Marshal(targets)
		t.Fatal("catalog metadata overrode real run host", string(body), err)
	}
}
