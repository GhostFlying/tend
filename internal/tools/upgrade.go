package tools

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// startHostedUpgrade is called only after handleHosted validated the exact
// admitted main execution and rejected metadata repair. Delivery approval is
// independently rechecked by StartUpgrade at the verified-bundle boundary.
func (s *Service) startHostedUpgrade(ctx context.Context, c Call, active bool) (any, error) {
	if s.upgrades == nil {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "local upgrade controller unavailable")
	}
	var args protocol.UpgradeStartRequest
	if err := decode(c.Arguments, &args); err != nil {
		return nil, err
	}
	if args.DeliveryTaskID == "" {
		return nil, protocol.Errorf(protocol.CodeForbidden, "an approved delivery execution is required")
	}
	if args.RequestID == "" {
		args.RequestID = c.RequestID
		if args.RequestID == "" {
			if c.Native == "" {
				return nil, protocol.Errorf(protocol.CodeMissingRequestID, "missing original request identity")
			}
			var err error
			args.RequestID, err = s.app.NativeRequestID(ctx, "harness:"+c.Principal.Subject, c.Native)
			if err != nil {
				return nil, err
			}
		}
	}

	args.ExecutionRequestID = c.ExecutionRequestID
	if !active {
		recorded, err := s.app.HasRecordedUpgradeStart(ctx, args.RequestID)
		if err != nil {
			return nil, err
		}
		if !recorded {
			return nil, protocol.Errorf(protocol.CodeForbidden, "completed main execution cannot initiate a new upgrade")
		}
	}
	value, err := s.upgrades.Start(ctx, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"request_id": args.RequestID, "upgrade": value}, nil
}
