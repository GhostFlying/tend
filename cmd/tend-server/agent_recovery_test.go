package main

import "testing"

func TestAgentRecoveryConfig(t *testing.T) {
	zero, negative, big := int64(0), int64(-1), int64(86401)
	for _, tc := range []struct {
		config agentRecoveryConfig
		valid  bool
	}{
		{agentRecoveryConfig{}, true}, {agentRecoveryConfig{MaxAttempts: &zero}, true},
		{agentRecoveryConfig{MaxAttempts: &negative}, false}, {agentRecoveryConfig{BaseDelaySeconds: &zero}, false}, {agentRecoveryConfig{MaxDelaySeconds: &big}, false},
	} {
		_, err := tc.config.policy()
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc.config, err)
		}
	}
}
