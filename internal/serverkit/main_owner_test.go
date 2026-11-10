package serverkit

import (
	"slices"
	"testing"
)

func TestMainOwnerUsesEffectiveListenerOwners(t *testing.T) {
	cfg := Config{Owners: []string{"network-owner"}, LocalOwner: "development-owner"}
	if got := effectiveServerOwners(cfg, false); !slices.Equal(got, []string{"development-owner"}) {
		t.Fatal(got)
	}
	if got := effectiveServerOwners(cfg, true); !slices.Equal(got, cfg.Owners) {
		t.Fatal(got)
	}
	cfg.Tailnet = &struct {
		Hostname  string `json:"hostname"`
		Ephemeral bool   `json:"ephemeral"`
	}{Hostname: "service"}
	if got := effectiveServerOwners(cfg, false); !slices.Equal(got, cfg.Owners) {
		t.Fatal(got)
	}
}
