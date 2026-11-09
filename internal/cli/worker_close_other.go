//go:build !linux

package cli

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

func prepareWorkerClose(context.Context, workerCloseOptions, workerCloseHost) (protocol.WorkerCloseIntent, error) {
	return protocol.WorkerCloseIntent{}, errors.New("worker-close execution boundary observation requires Linux cgroup v2")
}

func confirmWorkerClose(context.Context, protocol.WorkerCloseIntent, string, workerCloseHost) (protocol.WorkerCloseProof, error) {
	return protocol.WorkerCloseProof{}, errors.New("worker-close execution boundary observation requires Linux cgroup v2")
}
