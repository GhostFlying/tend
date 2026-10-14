package app

import (
	"bytes"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// allowKinds authorizes principals by kind. Finer checks, such as a run
// acting only on itself, live in the commands.
func allowKinds(p protocol.Principal, kinds ...protocol.PrincipalKind) error {
	if slices.Contains(kinds, p.Kind) {
		return nil
	}
	return protocol.Errorf(protocol.CodeForbidden, "%s callers may not perform this operation", p.Kind)
}

// planners may create and steer tasks: the owner, the main agent and tend
// itself.
var planners = []protocol.PrincipalKind{protocol.PrincipalOwner, protocol.PrincipalMainAgent, protocol.PrincipalSystem}
