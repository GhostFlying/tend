package app

import (
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func validRunnerConfiguration(c protocol.RunnerConfiguration) bool {
	if !release.ValidSHA(c.PreviousSHA256) || !release.ValidSHA(c.SHA256) || c.PreviousSHA256 == c.SHA256 || len(c.Providers) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if (p != "github" && p != "codebase") || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

func runnerConfigurationMatches(h store.UpgradeHost, rt protocol.RunnerRuntime) bool {
	c := h.Configuration
	if c == nil {
		return true
	}
	if rt.ConfigurationSHA256 != c.SHA256 {
		return false
	}
	for _, p := range c.Providers {
		capability := protocol.CapabilityDeliveryGitHub
		if p == "codebase" {
			capability = protocol.CapabilityDeliveryCodebase
		}
		if !slices.Contains(rt.Capabilities, capability) {
			return false
		}
	}
	return true
}
