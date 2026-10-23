package runner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// freshHello rebuilds only a positively rejected stale observation. Transport
// uncertainty remains inside hello's same-envelope retry; a retired incarnation
// or any other conflict never grants permission to replay reconciliation.
func (r *Runner) freshHello(ctx context.Context) error {
	for {
		err := r.hello(ctx)
		var response *protocol.Error
		if !errors.As(err, &response) || response.Code != protocol.CodeConflictPayload {
			return err
		}
		var details struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(response.Details, &details) != nil || details.Reason != "stale_sequence" {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.cfg.RetryDelay):
		}
	}
}
