package agentconfig

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHostedCLIIsExplicitOptionalMetadata(t *testing.T) {
	for _, cli := range []string{"", "codex", "traex"} {
		t.Run(cli, func(t *testing.T) {
			cfg := fixture()
			cfg.FormatVersion = 2
			cfg.Runtimes = cfg.Runtimes[2:]
			cfg.Agents = cfg.Agents[2:]
			// A legacy-looking binding name is not the CLI declaration.
			cfg.Runtimes[0].ID = "traex"
			cfg.Runtimes[0].CLI = cli
			cfg.Agents[0].RuntimeID = "traex"
			projection := requireValid(t, "server", cfg).Public()
			if _, _, err := protocol.CanonicalAgentInventory(projection); err != nil {
				t.Fatalf("public declaration rejected: %v", err)
			}
			if runtime := projection.Runtimes[0]; runtime.CLI != cli || runtime.Availability.Available {
				t.Fatalf("declaration changed or inferred readiness: %+v", runtime)
			}
		})
	}
	for _, version := range []uint64{1, 2} {
		cfg := fixture()
		cfg.FormatVersion = version
		cfg.Runtimes[2].CLI = "arbitrary-cli"
		_, err := Validate("server", cfg, source())
		requireError(t, err, "unsupported_cli", "$.runtimes[2].cli")
	}
}
