package agentconfig

import (
	"bytes"
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
	d := requireValid(t, "host", cfg)
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
