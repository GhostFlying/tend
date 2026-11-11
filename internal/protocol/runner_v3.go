package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// tend:compat runner-v3 protocol=3 remove=5
// Protocol 3 allocated claims through request identity. Adapt that identity to
// the actual Run primary key once, without retaining an alias or request record.

func AdaptRunnerClaim(env Envelope) (Envelope, error) {
	var p ClaimPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return env, Errorf(CodeInvalidRequest, "claim payload is malformed")
	}
	version, err := RuntimeRunnerProtocol(p.Runtime)
	if err != nil {
		return env, Errorf(CodeInvalidRequest, "runner protocol is unsupported")
	}
	if p.RunID == "" && version == 3 && ValidID(PrefixRequest, env.RequestID) {
		p.RunID = string(PrefixRun) + "_" + strings.TrimPrefix(env.RequestID, string(PrefixRequest)+"_")
		env.Payload, err = json.Marshal(p)
		if err != nil {
			return env, fmt.Errorf("encode adapted claim: %w", err)
		}
	}
	if !ValidID(PrefixRun, p.RunID) {
		return env, Errorf(CodeInvalidRequest, "claim requires its stable run_id")
	}
	return env, nil
}

// RunnerProtocolSupported is deliberately a two-generation communication gate.
func RunnerProtocolSupported(version int64) bool {
	return version >= PreviousRunnerProtocol && version <= CurrentRunnerProtocol
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

// Upgrades are server-first: this adapter accepts old runners on a new server;
// new runners never silently retry requests against an older server.
// RuntimeRunnerProtocol adapts unversioned bootstrap runners at one boundary.
func RuntimeRunnerProtocol(runtime *RunnerRuntime) (int64, error) {
	if runtime == nil {
		return 0, fmt.Errorf("unversioned runner protocol is retired")
	}
	return NegotiatedRunnerProtocol(runtime.ProtocolMin, runtime.ProtocolMax)
}

// AdaptRunnerInventoryStatus keeps the old transport-receipt route distinct
// from the catalog's real Current/Projection business facts. Protocol 3 can
// safely retry its original internal registration by immutable snapshot key.
func AdaptRunnerInventoryStatus(runtime *RunnerRuntime, status AgentInventoryStatus) AgentInventoryStatus {
	if runtime != nil && runtime.Protocol == 3 && status.RequestState == "business_state" {
		status.RequestState = "not_recorded"
		status.Request = nil
		status.RequestPayloadHash = ""
		status.Matched = nil
	}
	return status
}
