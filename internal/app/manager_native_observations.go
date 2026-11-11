package app

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Native RPC responses are in-flight transport data. Business completion is
// committed separately to the original Run or tool call before notifying waiters.
type nativePendingCall struct {
	run      string
	request  protocol.ManagerNativeRequest
	ctx      context.Context
	consumed bool
	reply    *protocol.ManagerNativeReply
}
type nativeCallBroker struct {
	mu    sync.Mutex
	calls map[string]*nativePendingCall
	order []string
}

func (b *nativeCallBroker) prune() {
	keep := b.order[:0]
	for _, id := range b.order {
		p := b.calls[id]
		if p == nil {
			continue
		}
		if p.ctx.Err() != nil {
			delete(b.calls, id)
			continue
		}
		keep = append(keep, id)
	}
	b.order = keep
}
func (b *nativeCallBroker) add(ctx context.Context, run string, p protocol.ManagerNativeRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune()
	if len(b.calls) >= 128 {
		for _, id := range b.order {
			if entry := b.calls[id]; entry != nil && entry.consumed {
				delete(b.calls, id)
				break
			}
		}
		b.prune()
	}
	if len(b.calls) >= 128 {
		return protocol.Errorf(protocol.CodeInvalidTransition, "too many in-flight native calls")
	}
	if b.calls == nil {
		b.calls = map[string]*nativePendingCall{}
	}
	if old := b.calls[p.ID]; old != nil {
		return nil
	}
	b.calls[p.ID] = &nativePendingCall{run: run, request: p, ctx: ctx}
	b.order = append(b.order, p.ID)
	return nil
}
func (b *nativeCallBroker) remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.calls, id)
	b.prune()
}
func (b *nativeCallBroker) claim(run string) *protocol.ManagerNativeRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune()
	for _, id := range b.order {
		p := b.calls[id]
		if p.run == run && managerNativeObservationMethod(p.request.Method) && !p.consumed && p.reply == nil {
			v := p.request
			return &v
		}
	}
	return nil
}
func (b *nativeCallBroker) observation(run, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.calls[id]
	return p != nil && p.run == run && managerNativeObservationMethod(p.request.Method)
}
func (b *nativeCallBroker) deliver(run string, reply protocol.ManagerNativeReply) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.calls[reply.ID]
	if p != nil && p.run == run && !p.consumed {
		p.reply = &reply
	}
}
func (b *nativeCallBroker) result(run, id string) (protocol.ManagerNativeReply, bool, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune()
	p := b.calls[id]
	if p == nil || p.run != run || p.consumed {
		return protocol.ManagerNativeReply{}, false, false
	}
	if p.reply == nil {
		return protocol.ManagerNativeReply{}, false, true
	}
	reply := *p.reply
	// Only bounded, volatile delivery identity survives consumption, so a
	// previous-version runner can recover its lost HTTP ACK. No body survives.
	p.request.Payload = nil
	p.reply = nil
	p.consumed = true
	return reply, true, true
}

func (a *App) nativeObservation(ctx context.Context, x *Exec, cmd Command) (protocol.Operation, bool, error) {
	operation := cmd.Operation()
	if operation != protocol.OpManagerNativeClaim && operation != protocol.OpManagerNativeReply {
		return protocol.Operation{}, false, nil
	}
	if operation == protocol.OpManagerNativeClaim {
		var p protocol.ManagerNativeClaimPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return protocol.Operation{}, true, err
		}
		if len(p.AcknowledgedIDs) > 0 || p.AcknowledgeOnly {
			return protocol.Operation{}, false, nil
		}
	}
	var reply protocol.ManagerNativeReply
	if operation == protocol.OpManagerNativeReply {
		if err := decodePayload(x.Env, &reply); err != nil {
			return protocol.Operation{}, true, err
		}
		if !a.nativeCalls.observation(x.Env.Target.ID, reply.ID) && !managerNativeObservationMethod(reply.Method) {
			return protocol.Operation{}, false, nil
		}
	}
	var result any
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		if err := cmd.Authorize(ctx, x, tx.Q()); err != nil {
			return err
		}
		if operation == protocol.OpManagerNativeClaim {
			if request := a.nativeCalls.claim(x.Env.Target.ID); request != nil {
				result = request
			}
		} else {
			a.nativeCalls.deliver(x.Env.Target.ID, reply)
			result = reply
		}
		return nil
	})
	if err != nil {
		return protocol.Operation{}, true, err
	}
	if result == nil {
		return protocol.Operation{}, false, nil
	}
	raw, err := json.Marshal(result)
	if operation == protocol.OpManagerNativeReply {
		a.changes.Notify()
	}
	return protocol.Operation{RequestID: x.Env.RequestID, Status: protocol.OpSucceeded, Result: raw}, true, err
}
