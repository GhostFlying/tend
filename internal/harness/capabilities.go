package harness

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// CapabilityWorkers is an optional trusted adapter contract. Inventory must be
// read-only, bounded and free of model calls. Profile evidence is permissible
// only when the adapter can authoritatively describe every future session for
// this exact configuration. A session-only tool list does not implement that
// guarantee. StartWithCapabilities must check provenance at session creation,
// before any business/model turn, and fail on a changed tool/configuration.
// Ordinary Workers do not implement this interface and remain unsupported.
type CapabilityWorkers interface {
	Workers
	CapabilityInventory(context.Context, protocol.RunExecutionPin, protocol.CapabilityReference) (protocol.ExecutorCapabilityEvidence, error)
	StartWithCapabilities(context.Context, WorkerSpec, protocol.RunCapabilityBinding) (Worker, error)
}
