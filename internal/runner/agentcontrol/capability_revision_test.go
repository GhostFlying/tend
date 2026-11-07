package agentcontrol_test

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestLocalAgentsCapabilityRevision(t *testing.T) {
	one := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	two := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 2}}
	for _, tc := range []struct {
		name          string
		before, after []protocol.CapabilityReference
		stripVersion  bool
	}{{"add", nil, one, false}, {"remove", one, nil, false}, {"replace", one, two, false}, {"extension", one, nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.config.CapabilityVersion = 1
			f.config.Agents[0].Capabilities = tc.before
			f.write()
			stop := f.start()
			defer stop()
			firstID := f.ids.New(protocol.PrefixRequest)
			first, code, stderr := f.command("reload", firstID, revision(0))
			if code != 0 || first.Receipt == nil {
				t.Fatal(first, code, stderr)
			}
			f.config.Agents[0].Capabilities = tc.after
			if tc.stripVersion {
				f.config.CapabilityVersion = 0
			}
			f.write()
			bad, code, stderr := f.command("reload", f.ids.New(protocol.PrefixRequest), revision(1))
			if code != 1 || bad.ErrorCode != "capability_revision_conflict" || bad.Current.Revision != 1 {
				t.Fatal("CLI reload accepted reused declaration revision", bad, code, stderr)
			}
			f.config.Agents[0].Revision++
			f.write()
			bumped, code, stderr := f.command("reload", f.ids.New(protocol.PrefixRequest), revision(1))
			if code != 0 || bumped.Receipt == nil || bumped.Receipt.Revision != 2 {
				t.Fatal("CLI rejected declaration revision bump", bumped, code, stderr)
			}
			f.config.Agents[0].Revision = 1
			f.config.Agents[0].Capabilities = tc.before
			f.config.CapabilityVersion = 1
			f.write()
			replay, code, stderr := f.command("reload", firstID, revision(0))
			if code != 0 || replay.Receipt == nil || *replay.Receipt != *first.Receipt || replay.Current.Revision != 2 {
				t.Fatal("CLI replay changed historical receipt or current state", replay, code, stderr)
			}
		})
	}
}
