package serverkit

import "testing"

func TestAgentRecoveryConfig(t *testing.T) {
	zero, negative, big := int64(0), int64(-1), int64(86401)
	for _, tc := range []struct {
		config AgentRecoveryConfig
		valid  bool
	}{
		{AgentRecoveryConfig{}, true}, {AgentRecoveryConfig{MaxAttempts: &zero}, true},
		{AgentRecoveryConfig{MaxAttempts: &negative}, false}, {AgentRecoveryConfig{BaseDelaySeconds: &zero}, false}, {AgentRecoveryConfig{MaxDelaySeconds: &big}, false},
	} {
		_, err := tc.config.policy()
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc.config, err)
		}
	}
}
