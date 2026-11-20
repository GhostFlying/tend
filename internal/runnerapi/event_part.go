package runnerapi

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) receiveEventPart(ctx context.Context, principal protocol.Principal, env *protocol.Envelope) (protocol.Operation, error) {
	var part protocol.RunEventPart
	if err := decodeStrict(env.Payload, &part); err != nil {
		return protocol.Operation{}, err
	}
	select {
	case s.originals <- struct{}{}:
		defer func() { <-s.originals }()
	case <-ctx.Done():
		return protocol.Operation{}, ctx.Err()
	}
	next, raw, key, err := s.app.StageRunEventPart(ctx, principal, env.Target, part)
	if err != nil {
		return protocol.Operation{}, err
	}
	receipt := protocol.RunEventPartReceipt{Next: next, SHA256: part.SHA256}
	if raw != nil {
		original := *env
		original.Operation = part.Operation
		original.Payload = raw
		op, err := s.dispatch(ctx, principal, &original)
		if err != nil || op.Status != protocol.OpSucceeded {
			return op, err
		}
		receipt.Stored = true
		if err := s.app.ForgetCommittedEventTransfer(key); err != nil {
			s.log.WarnContext(ctx, "event transfer cleanup pending", "run_id", env.Target.ID, "err", err)
		}
	}
	return withResult(protocol.Operation{Status: protocol.OpSucceeded, RequestID: env.RequestID}, receipt)
}
