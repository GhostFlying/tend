package cli

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityAgentCatalogPreservesContinuationFacts(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "runner-one", LocalID: "cedar"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "native-a", Revision: 1}, InventoryRevision: 1, SnapshotID: "original", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model", Effort: "high"}, Worker: protocol.WorkerAppServer, BackendID: "git"}
	want := protocol.RunnerAgentCatalog{HostID: "runner-one", Online: true, ContinuationSupported: true,
		RetainedDefinitions: []protocol.RetainedAgentDefinition{{Pin: pin, Definition: protocol.AgentInventoryDefinition{Identity: pin.Identity, Revision: 1, DisplayName: "Original Cedar", Binding: pin.Runtime, Categories: []string{"implementation"}, Purposes: []string{"worker_run"}, RequestedModel: "model", RequestedEffort: "high", Enabled: true}, Runtimes: []protocol.AgentInventoryRuntime{{RuntimeID: "native-a", Revision: 1, Kind: "appserver-worker", CLI: "codex", DeclaredPurposes: []string{"worker_run"}, Enabled: true}}}},
		RetainedExecutions:  []protocol.RunExecutionPin{pin}, Continuations: []protocol.AgentExecutor{{Pin: pin, Available: true}},
	}
	newer := want.RetainedDefinitions[0]
	newer.Pin.AgentRevision, newer.Pin.InventoryRevision, newer.Pin.SnapshotID = 2, 2, "second"
	newer.Pin.Runtime.Revision = 2
	newer.Pin.PublicSHA256 = "sha256:" + strings.Repeat("b", 64)
	newer.Definition.Revision, newer.Definition.DisplayName, newer.Definition.Binding.Revision = 2, "Newer Cedar", 2
	newer.Runtimes = slices.Clone(newer.Runtimes)
	newer.Runtimes[0].Revision, newer.Runtimes[0].CLI = 2, "traex"
	want.RetainedDefinitions = append(want.RetainedDefinitions, newer)
	want.RetainedExecutions = append(want.RetainedExecutions, newer.Pin)
	want.Continuations = append(want.Continuations, protocol.AgentExecutor{Pin: newer.Pin, Available: false, Reason: "original_execution_unavailable"})
	f.answer("GET /api/v1/hosts/runner-one/agents", want)
	if code := te.main("agents", "show", "--host", "runner-one", "--json"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var got protocol.RunnerAgentCatalog
	if err := json.Unmarshal(te.out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CLI dropped retained directory facts: got=%+v want=%+v", got, want)
	}
	if len(f.seen("GET /api/v1/hosts/runner-one/agents")) != 1 || len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("directory command did not remain an explicit read-only entry")
	}
}
