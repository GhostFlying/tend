package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNamedIssueOwnerCLIAllowsConcreteAgentWithoutCategory(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	request := te.IDs.New(protocol.PrefixRequest)
	if code := te.main("issue", "request-owner", "issue-original", "--revision", "7", "--epoch", "3", "--request-id", request, "--data", `{"agent_id":"existing-env/ordinary","reason":"Investigate using the same primary"}`, "--dry-run"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var env protocol.Envelope
	var payload protocol.RequestIssueManagerPayload
	if json.Unmarshal(te.out.Bytes(), &env) != nil || json.Unmarshal(env.Payload, &payload) != nil || payload.AgentID != "existing-env/ordinary" || payload.CategoryID != "" || payload.ExpectedAssignmentEpoch != 3 || *env.ExpectedControlRevision != 7 || env.RequestID != request || env.Operation != protocol.OpIssueRequestOwner {
		t.Fatal("named CLI replaced concrete responsibility with a category", te.out.String())
	}
	if len(f.requests) != 0 {
		t.Fatal("explicit dry run performed a write or refreshed original control values")
	}
	if help := commandUsage([]string{"issue", "request-owner"}); !strings.Contains(help, "category_id is optional") || !strings.Contains(help, "Entry, CLI, model and effort never change") {
		t.Fatal(help)
	}
}

func TestInternalArtifactHandoffCLIExportsCompleteOriginalPayload(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("issue", "share-artifact", "issue-parent", "--revision", "7", "--epoch", "3", "--data", `{"internal_handoff":true,"artifact_id":"art_original","ref_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","reason":"Consume exact child evidence"}`, "--dry-run"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var env protocol.Envelope
	var payload protocol.GrantArtifactSharePayload
	if json.Unmarshal(te.out.Bytes(), &env) != nil || json.Unmarshal(env.Payload, &payload) != nil || !payload.InternalHandoff || payload.ArtifactID != "art_original" || payload.ExpectedAssignmentEpoch != 3 || payload.SourceMessageID != "" || env.Operation != protocol.OpGrantArtifactShare {
		t.Fatal(te.out.String())
	}
	if len(f.requests) != 0 {
		t.Fatal("dry run performed a mutation")
	}
}
