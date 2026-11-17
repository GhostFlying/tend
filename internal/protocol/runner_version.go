package protocol

import (
	"encoding/json"
	"fmt"
)

func ValidateRunnerClaim(env Envelope) (Envelope, error) {
	var p ClaimPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return env, Errorf(CodeInvalidRequest, "claim payload is malformed")
	}
	_, err := RuntimeRunnerProtocol(p.Runtime)
	if err != nil {
		return env, Errorf(CodeInvalidRequest, "runner protocol is unsupported")
	}
	if !ValidID(PrefixRun, p.RunID) {
		return env, Errorf(CodeInvalidRequest, "claim requires its stable run_id")
	}
	return env, nil
}

// RunnerProtocolSupported enforces the current installation compatibility floor.
func RunnerProtocolSupported(version int64) bool {
	return version >= MinimumRunnerProtocol && version <= CurrentRunnerProtocol
}

// NegotiatedRunnerProtocol selects the newest common supported generation.
func NegotiatedRunnerProtocol(minimum, maximum int64) (int64, error) {
	if minimum < 0 || maximum < minimum {
		return 0, fmt.Errorf("invalid runner protocol range")
	}
	selected := min(maximum, CurrentRunnerProtocol)
	if selected < minimum || !RunnerProtocolSupported(selected) {
		return 0, fmt.Errorf("runner protocol range has no supported generation")
	}
	return selected, nil
}

// RuntimeRunnerProtocol validates an explicitly versioned runtime.
func RuntimeRunnerProtocol(runtime *RunnerRuntime) (int64, error) {
	if runtime == nil {
		return 0, fmt.Errorf("unversioned runner protocol is retired")
	}
	return NegotiatedRunnerProtocol(runtime.ProtocolMin, runtime.ProtocolMax)
}

func ValidateRunnerTakeover(env Envelope, version int64) (Envelope, error) {
	if !RunnerProtocolSupported(version) {
		return env, Errorf(CodeInvalidRequest, "runner protocol is unsupported")
	}
	var p TakeoverPayload
	if json.Unmarshal(env.Payload, &p) != nil {
		return env, Errorf(CodeInvalidRequest, "takeover payload is malformed")
	}
	if !ValidID(PrefixRequest, p.ObservationID) {
		return env, Errorf(CodeInvalidRequest, "takeover requires its stable observation_id")
	}
	return env, nil
}
