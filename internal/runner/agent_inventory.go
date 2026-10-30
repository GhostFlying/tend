package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func (r *Runner) inventoryRuntime() (*protocol.RunnerRuntime, error) {
	r.controlMu.Lock()
	supported := r.agentInventorySupported
	r.controlMu.Unlock()
	r.mu.Lock()
	runtime := r.confirmedRuntime
	r.mu.Unlock()
	if !supported || !slices.Contains(runtime.Capabilities, protocol.CapabilityAgentInventory) {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "agent inventory has not been negotiated by hello")
	}
	return &runtime, nil
}

func (r *Runner) readAgentInventory(ctx context.Context, runtime *protocol.RunnerRuntime, requestID string) (protocol.AgentInventoryStatus, error) {
	env, err := r.newEnvelope(protocol.OpRunnerInventoryStatus, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.AgentInventoryStatusPayload{Runtime: runtime, RequestID: requestID})
	if err != nil {
		return protocol.AgentInventoryStatus{}, err
	}
	op, err := r.c.do(ctx, r.c.host(), env)
	if err != nil {
		return protocol.AgentInventoryStatus{}, err
	}
	if op.Error != nil {
		return protocol.AgentInventoryStatus{}, op.Error
	}
	var status protocol.AgentInventoryStatus
	if err := json.Unmarshal(op.Result, &status); err != nil {
		return status, &transportError{err}
	}
	return status, nil
}

func (r *Runner) pendingInventory(ctx context.Context) (*protocol.Envelope, error) {
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return nil, err
	}
	for _, request := range requests {
		if request.Envelope.Operation == protocol.OpRunnerInventoryRegister {
			return &request.Envelope, nil
		}
	}
	return nil, nil
}

// RegisterAgentInventory publishes only the accepted public snapshot. Journaled
// unknown requests keep their exact runtime, payload, request ID, and CAS.
func (r *Runner) RegisterAgentInventory(ctx context.Context) error {
	if r.cfg.AgentInventory == nil {
		return nil
	}
	r.inventoryMu.Lock()
	defer r.inventoryMu.Unlock()
	runtime, err := r.inventoryRuntime()
	if err != nil {
		return err
	}
	pending, err := r.pendingInventory(ctx)
	if err != nil {
		return err
	}
	var status protocol.AgentInventoryStatus
	if pending != nil {
		status, err = r.readAgentInventory(ctx, runtime, pending.RequestID)
		if err != nil {
			return err
		}
		var payload protocol.RegisterAgentInventoryPayload
		if err := json.Unmarshal(pending.Payload, &payload); err != nil {
			return fmt.Errorf("runner: decode pending inventory: %w", err)
		}
		switch status.RequestState {
		case "recorded":
			hash, err := protocol.PayloadHash(pending)
			if err != nil {
				return err
			}
			if status.Request == nil || status.Request.RequestID != pending.RequestID || status.RequestPayloadHash != hash {
				return protocol.Errorf(protocol.CodeConflictPayload, "inventory request reconciliation does not match the journal")
			}
			if err := r.finishInventory(ctx, pending, *status.Request); err != nil {
				return err
			}
		case "not_recorded":
			if payload.Runtime == nil {
				return protocol.Errorf(protocol.CodeInvalidRequest, "pending inventory has no runtime")
			}
			if payload.Runtime.InstanceID == runtime.InstanceID && payload.Runtime.Generation == runtime.Generation {
				// An older HTTP attempt may still commit; only its original envelope is safe.
				if err := r.sendInventory(ctx, pending); err != nil {
					return err
				}
				status, err = r.readAgentInventory(ctx, runtime, "")
				if err != nil {
					return err
				}
				break
			}
			// A successful hello from the journal's strictly newer process generation
			// fences the predecessor inside the same server writer used for inventory.
			// A repeated hello of the same incarnation is never such a fence.
			if payload.Runtime.InstanceID == runtime.InstanceID || payload.Runtime.Generation >= runtime.Generation {
				return protocol.Errorf(protocol.CodeConflictPayload, "inventory predecessor has not been fenced by a newer process")
			}
			r.cfg.Log.InfoContext(ctx, "predecessor inventory was not recorded after new-process hello", "request_id", pending.RequestID)
			if err := r.cfg.Journal.DeleteRequest(ctx, pending.RequestID); err != nil {
				return err
			}
		default:
			return &transportError{errors.New("inventory request outcome is not confirmed")}
		}
	} else {
		status, err = r.readAgentInventory(ctx, runtime, "")
		if err != nil {
			return err
		}
	}
	local, err := r.cfg.AgentInventory(ctx)
	if err != nil {
		return err
	}
	if local.Revision == 0 || local.Projection == nil || (local.Projection.FormatVersion == 2 && runtime.Protocol < 2) {
		return nil
	}
	if local.Revision > math.MaxInt64 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "local inventory revision exceeds wire range")
	}
	if status.Current != nil && status.Current.LocalRevision == int64(local.Revision) && status.Current.SnapshotID == local.SnapshotID && status.Current.PublicSHA256 == local.PublicSHA256 {
		return nil
	}
	revision := int64(0)
	if status.Current != nil {
		revision = status.Current.ServerRevision
	}
	env, err := r.newEnvelope(protocol.OpRunnerInventoryRegister, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: int64(local.Revision), SnapshotID: local.SnapshotID, PublicSHA256: local.PublicSHA256, Projection: *local.Projection})
	if err != nil {
		return err
	}
	env.ExpectedControlRevision = &revision
	if err := r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: *env}); err != nil {
		return err
	}
	return r.sendInventory(ctx, env)
}

func (r *Runner) sendInventory(ctx context.Context, env *protocol.Envelope) error {
	// One attempt lets metadata uncertainty return to the existing reconciler,
	// rather than holding worker reconciliation in an unbounded metadata retry.
	op, err := r.c.do(ctx, r.c.host(), env)
	if err != nil {
		// A retired process may be denied even when its earlier attempt was
		// accepted. Only request-status reconciliation can settle that original
		// effect; authorization failure is not an inventory receipt.
		return err
	}
	return r.finishInventory(ctx, env, op)
}

func (r *Runner) finishInventory(ctx context.Context, env *protocol.Envelope, op protocol.Operation) error {
	if op.Status == protocol.OpFailed && op.Error != nil {
		return errors.Join(op.Error, r.cfg.Journal.DeleteRequest(ctx, env.RequestID))
	}
	if op.Status != protocol.OpSucceeded {
		return &transportError{errors.New("inventory outcome is not final")}
	}
	var receipt protocol.AgentInventoryReceipt
	var payload protocol.RegisterAgentInventoryPayload
	if err := json.Unmarshal(op.Result, &receipt); err != nil {
		return &transportError{err}
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return err
	}
	if receipt.HostID != r.cfg.HostID || receipt.LocalRevision != payload.LocalRevision || receipt.SnapshotID != payload.SnapshotID || receipt.PublicSHA256 != payload.PublicSHA256 || receipt.State != "registered_staged" {
		return protocol.Errorf(protocol.CodeConflictPayload, "inventory receipt does not match the journal")
	}
	return r.cfg.Journal.DeleteRequest(ctx, env.RequestID)
}

// AgentRegistrationStatus reads server receipt and unresolved original request;
// it does not refresh presence, re-register, or make a runtime available.
func (r *Runner) AgentRegistrationStatus(ctx context.Context) (protocol.LocalAgentRegistration, error) {
	r.inventoryMu.Lock()
	defer r.inventoryMu.Unlock()
	result := protocol.LocalAgentRegistration{State: "awaiting_hello"}
	runtime, err := r.inventoryRuntime()
	if err != nil {
		return result, err
	}
	pending, err := r.pendingInventory(ctx)
	if err != nil {
		return result, err
	}
	if pending != nil {
		result.PendingRequestID = pending.RequestID
	}
	status, err := r.readAgentInventory(ctx, runtime, result.PendingRequestID)
	if err != nil {
		result.State = "unknown"
		return result, err
	}
	result.State = "not_registered"
	result.Server = &status
	if status.Current != nil {
		result.State = status.Current.State
	}
	local, err := r.cfg.AgentInventory(ctx)
	if err != nil {
		return result, err
	}
	if local.Revision > 0 && (status.Current == nil || status.Current.LocalRevision != int64(local.Revision) || status.Current.SnapshotID != local.SnapshotID || status.Current.PublicSHA256 != local.PublicSHA256) {
		result.State = "local_accepted"
	}

	if pending != nil {
		result.State = "pending"
	}
	return result, nil
}

// AgentInventoryHandler extends the existing private control listener only.
func (r *Runner) AgentInventoryHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		register := q.Method == http.MethodPost && q.URL.Path == "/agents/register"
		status := q.Method == http.MethodGet && q.URL.Path == "/agents/registration"
		if !register && !status {
			next.ServeHTTP(w, q)
			return
		}
		ctx, cancel := context.WithTimeout(q.Context(), 30*time.Second)
		defer cancel()
		var err error
		if register {
			err = r.RegisterAgentInventory(ctx)
		}
		result, readErr := r.AgentRegistrationStatus(ctx)
		if err == nil {
			err = readErr
		}
		out := protocol.LocalAgentConfigurationResult{Registration: &result}
		code := http.StatusOK
		if err != nil {
			code = http.StatusConflict
			out.ErrorCode = "registration_unknown"
			if e, ok := errors.AsType[*protocol.Error](err); ok {
				out.ErrorCode = string(e.Code)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
	})
}
