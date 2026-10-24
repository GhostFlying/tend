package main

import (
	"fmt"
	"os/exec"
	"slices"

	"github.com/GhostFlying/tend/internal/reviewwatch"
	"github.com/GhostFlying/tend/internal/runner"
)

// Observation is opt-in per provider and independent of worker.kind.
type deliveryObserverConfig struct {
	Providers   []string `json:"providers"`
	GitHubBin   string   `json:"gh_bin"`
	CodebaseBin string   `json:"bytedcli_bin"`
}

func (c deliveryObserverConfig) build() (runner.DeliveryObserver, []string, error) {
	if len(c.Providers) == 0 {
		return nil, nil, nil
	}
	providers := []string{}
	for _, provider := range c.Providers {
		bin := ""
		switch provider {
		case reviewwatch.GitHub:
			bin = c.GitHubBin
			if bin == "" {
				bin = "gh"
			}
		case reviewwatch.Codebase:
			bin = c.CodebaseBin
			if bin == "" {
				bin = "bytedcli"
			}
		default:
			return nil, nil, fmt.Errorf("unsupported delivery observer provider")
		}
		if _, err := exec.LookPath(bin); err != nil {
			return nil, nil, fmt.Errorf("delivery observer CLI for %s is unavailable", provider)
		}
		if !slices.Contains(providers, provider) {
			providers = append(providers, provider)
		}
	}
	return reviewwatch.New(reviewwatch.Config{GitHubBin: c.GitHubBin, CodebaseBin: c.CodebaseBin}), providers, nil
}
