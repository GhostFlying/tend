package app

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// StageRunEventPart only stages bytes, bound to the original Run and operation.
// The ordinary event handler verifies and commits the reassembled original.
func (a *App) StageRunEventPart(ctx context.Context, p protocol.Principal, target protocol.Target, part protocol.RunEventPart) (int64, []byte, string, error) {
	if p.Kind != protocol.PrincipalRun || target.Type != "run" || p.Subject != target.ID {
		return 0, nil, "", protocol.Errorf(protocol.CodeForbidden, "run event part requires its own Run")
	}
	operation := protocol.OpRunnerSyncEvents
	if p.Role == protocol.RoleIssueManager {
		operation = protocol.OpManagerNativeEvent
	}
	if part.Operation != operation {
		return 0, nil, "", protocol.Errorf(protocol.CodeForbidden, "invalid run event operation")
	}
	digest, err := hex.DecodeString(part.SHA256)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != part.SHA256 || len(part.Data) == 0 || len(part.Data) > protocol.RunEventPartBytes || part.Offset < 0 || part.Size <= part.Offset || int64(len(part.Data)) > part.Size-part.Offset {
		return 0, nil, "", protocol.Errorf(protocol.CodeInvalidRequest, "invalid run event part")
	}
	key := fmt.Sprintf("%s/%s/%s/%d", target.ID, operation, part.SHA256, part.Size)
	n, raw, err := a.store.FileStore().ReceivePart(ctx, key, part.SHA256, part.Size, part.Offset, part.Data)
	return n, raw, key, err
}

func (a *App) ForgetCommittedEventTransfer(key string) error {
	return a.store.FileStore().ForgetTransfer(key)
}
