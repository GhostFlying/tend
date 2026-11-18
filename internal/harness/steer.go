package harness

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ErrSteerUnsupported accompanies a definite rejection by an older executor.
var ErrSteerUnsupported = errors.New(protocol.ManagerSteerUnsupported)

// TurnSteerer accepts real user input in an existing active turn. The expected
// turn is a mandatory fence: implementations must never start another turn.
// Success proves acceptance, not model consumption. An error other than
// ErrRejected is unknown and must not be automatically resent.
type TurnSteerer interface {
	SteerTurn(ctx context.Context, expectedTurnID string, in TurnInput) error
}
