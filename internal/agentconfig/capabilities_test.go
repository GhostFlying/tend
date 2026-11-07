package agentconfig

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilitiesRemainDeclarationsAndDetachSnapshots(t *testing.T) {
	cfg := fixture()
	old := requireValid(t, "host", cfg)
	oldBytes, oldHash := publicBytes(t, old)
	if bytes.Contains(oldBytes, []byte("capability")) {
		t.Fatal("legacy hash domain changed")
	}
	cfg.CapabilityVersion = 1
	cfg.Agents[0].Capabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	// Duplicate profile tuples are still distinct author-chosen identities.
	copyAgent := cfg.Agents[0]
	copyAgent.LocalID = "another"
	cfg.Agents = append(cfg.Agents, copyAgent)
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal("actual configuration decoder rejected declaration", err)
	}
	d := requireValid(t, "host", decoded)
	cfg.Agents[0].Capabilities[0].DefinitionRevision = 99
	p := d.Public()
	if p.Agents[0].Capabilities[0].DefinitionRevision != 1 || p.Agents[0].Availability.Available {
		t.Fatal(p)
	}
	p.Agents[0].Capabilities[0].DefinitionRevision = 42
	if d.Public().Agents[0].Capabilities[0].DefinitionRevision != 1 {
		t.Fatal("public mutation altered accepted declaration")
	}
	_, newHash := publicBytes(t, d)
	if oldHash == newHash {
		t.Fatal("declaration missing from canonical hash")
	}
	if _, hash := publicBytes(t, old); hash != oldHash {
		t.Fatal("old snapshot mutated")
	}
	cfg.CapabilityVersion = 0
	if _, err := Validate("host", cfg, source()); err == nil {
		t.Fatal("unversioned extension accepted")
	}
}

func TestCapabilityConfigurationExplicitBatchAndNumericRevisionValidation(t *testing.T) {
	cfg := fixture()
	cfg.CapabilityVersion = 1
	cfg.Agents[1].RuntimeID = cfg.Agents[0].RuntimeID
	cfg.Agents[1].Model = cfg.Agents[0].Model
	cfg.Agents[1].Effort = cfg.Agents[0].Effort
	refs := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	cfg.Agents[0].Capabilities = refs
	cfg.Agents[1].Capabilities = refs
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	public := requireValid(t, "host", decoded).Public()
	count := 0
	for _, agent := range public.Agents {
		if len(agent.Capabilities) > 0 {
			count++
		}
	}
	if count != 2 {
		t.Fatal("explicit batch not preserved", public)
	}
	// The unselected Agent has no implicit capability despite its runtime/model.
	for _, agent := range public.Agents {
		if agent.Identity.LocalID == "planner-chat" && len(agent.Capabilities) != 0 {
			t.Fatal("brand/category granted capability")
		}
	}
	for _, bad := range []string{`"1"`, `1.5`, `9223372036854775808`, `null`} {
		malformed := bytes.ReplaceAll(raw, []byte(`"definition_revision":1`), []byte(`"definition_revision":`+bad))
		if _, err := Decode(malformed); err == nil {
			t.Fatal("invalid definition revision decoded", bad)
		}
	}
	decoded.Agents[0].Capabilities[0].DefinitionRevision = -1
	if _, err := Validate("host", decoded, source()); err == nil {
		t.Fatal("negative definition revision accepted")
	}
}
