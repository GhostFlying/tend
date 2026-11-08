package harness

import (
	"context"
	"errors"
)

// ErrOwnedWorkerUnconfirmed retains an original native process when a bounded
// independent stop could not prove its closure. It grants no retry or replay.
var ErrOwnedWorkerUnconfirmed = errors.New("original owned worker closure is unconfirmed")

// OwnedWorkerStopper closes a locally owned native process using durable run
// identity. It cannot launch, resume or resend work. A missing or unverifiable
// identity must return an error, retaining the original run for reconciliation.
type OwnedWorkerStopper interface {
	SupportsOwnedWorkerStop() bool
	StopOwnedWorker(context.Context, WorkerSpec) error
}
