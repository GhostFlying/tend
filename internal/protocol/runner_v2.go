package protocol

import "fmt"

// tend:compat runner-v2 protocol=2 remove=4
// Protocol 2 retains the historical format-1/2 inventory and dual-entry pins.
// New single-entry inventories require protocol 3; old pins are never rewritten.

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
