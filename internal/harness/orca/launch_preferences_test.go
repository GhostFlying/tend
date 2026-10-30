package orca

import (
	"errors"
	"fmt"
	"testing"
)

func TestTraeModelSelectionRequiresCLIAndHost(t *testing.T) {
	for _, tc := range []struct {
		name, client, host string
		supported          bool
	}{
		{"both", `["agent.launch.trae-model.v1"]`, `["agent.launch.trae-model.v1"]`, true},
		{"old_cli_new_host", `[]`, `["agent.launch.trae-model.v1"]`, false},
		{"new_cli_old_host", `["agent.launch.trae-model.v1"]`, `[]`, false},
		{"old_both", `[]`, `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := newFake(map[string]string{"status --json": fmt.Sprintf(`{"ok":true,"result":{"client":{"capabilities":%s},"runtime":{"runtimeId":"runtime-1","capabilities":%s}}}`, tc.client, tc.host)})
			err := c.CheckTraeModelSelection(t.Context())
			if tc.supported && err != nil || !tc.supported && !errors.Is(err, ErrTraeModelSelectionUnsupported) {
				t.Fatalf("supported=%v err=%v", tc.supported, err)
			}
			if len(f.calls) != 1 {
				t.Fatal(f.calls)
			}
		})
	}
}

func TestTraeModelSelectionKeepsReadErrors(t *testing.T) {
	c, _ := newFake(map[string]string{"status --json": `{"ok":false,"error":{"code":"runtime_unavailable","message":"unavailable"}}`})
	if err := c.CheckTraeModelSelection(t.Context()); err == nil || errors.Is(err, ErrTraeModelSelectionUnsupported) {
		t.Fatal(err)
	}
	c, _ = newFake(map[string]string{"status --json": `{"ok":true,"result":{"runtime":{"runtimeId":null}}}`})
	if err := c.CheckTraeModelSelection(t.Context()); err == nil || errors.Is(err, ErrTraeModelSelectionUnsupported) {
		t.Fatal(err)
	}
}
