package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// recoverRunMessages replays server requests and retained receipts only. A native
// invocation whose outcome was lost is never repeated, including after pause.
func (r *Runner) recoverRunMessages(ctx context.Context, as protocol.Assignment) error {
	deliveries, err := r.cfg.Journal.PendingMessageDeliveries(ctx, as.RunID)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		if d.Report == nil {
			var m protocol.RunMessage
			if d.Invoked {
				// Invoked is persisted only after a confirmed claim. Its frozen
				// identity suffices to report unknown even after lease release.
				if err := json.Unmarshal(d.Claim.Payload, &m.RunMessageDelivery); err != nil {
					return err
				}
				m.Status = protocol.RunMessageDispatching
			} else {
				op, err := r.send(ctx, runCredential(as), &d.Claim)
				if op.Status == protocol.OpFailed {
					if err := r.cfg.Journal.SettleMessageDelivery(ctx, d.MessageID); err != nil {
						return err
					}
					continue
				}
				if err != nil {
					return err
				}
				if err := json.Unmarshal(op.Result, &m); err != nil {
					return err
				}
			}
			if m.Status != protocol.RunMessageDispatching {
				if err := r.cfg.Journal.SettleMessageDelivery(ctx, d.MessageID); err != nil {
					return err
				}
				continue
			}
			receipt := protocol.RunMessageReceipt{RunMessageDelivery: m.RunMessageDelivery, Status: protocol.RunMessageFailed, Reason: "worker_ended"}
			if d.Invoked {
				receipt.Status, receipt.Reason = protocol.RunMessageUnknown, "receiver_restarted"
			}
			d.Report, err = r.messageReceiptEnvelope(ctx, as, receipt)
			if err != nil {
				return err
			}
		}
		if err := r.sendMessageReceipt(ctx, as, d.MessageID, d.Report); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) messageReceiptEnvelope(ctx context.Context, as protocol.Assignment, p protocol.RunMessageReceipt) (*protocol.Envelope, error) {
	env, err := r.newEnvelope(protocol.OpRunsReportMessage, runTarget(as), p)
	if err != nil {
		return nil, err
	}
	original, err := r.cfg.Journal.SaveMessageReceipt(ctx, p.MessageID, *env)
	if err != nil {
		return nil, err
	}
	return &original, nil
}

// recoverFinalMessageReceipts is independent of execution and hello. A finished
// run's retained acceptance evidence survives final-report loss and restart.
func (r *Runner) recoverFinalMessageReceipts(ctx context.Context) error {
	assignments, err := r.cfg.Journal.MessageReceiptRuns(ctx)
	if err != nil {
		return err
	}
	for _, as := range assignments {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		// This path sends saved reports only, including their original IDs.
		deliveries, err := r.cfg.Journal.PendingMessageDeliveries(attemptCtx, as.RunID)
		if err == nil {
			for _, d := range deliveries {
				if d.Report != nil {
					if err = r.sendMessageReceipt(attemptCtx, as, d.MessageID, d.Report); err != nil {
						break
					}
				}
			}
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			r.cfg.Log.WarnContext(ctx, "retained run message receipt remains unresolved", "run_id", as.RunID, "code", protocol.CodeOf(err))
		}
	}
	return nil
}

func (r *Runner) finalMessageReceipts(ctx context.Context) {
	for ctx.Err() == nil {
		if err := r.recoverFinalMessageReceipts(ctx); err != nil && ctx.Err() == nil {
			r.cfg.Log.ErrorContext(ctx, "retained message receipt recovery failed", "code", protocol.CodeOf(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.cfg.RetryDelay):
		}
	}
}

func (r *Runner) sendMessageReceipt(ctx context.Context, as protocol.Assignment, id string, env *protocol.Envelope) error {
	_, err := r.send(ctx, runCredential(as), env)
	if err != nil {
		return err
	}
	return r.cfg.Journal.SettleMessageDelivery(ctx, id)
}

func (r *Runner) readPendingMessage(ctx context.Context, as protocol.Assignment, wait int) (protocol.RunMessagePage, error) {
	var page protocol.RunMessagePage
	err := r.retry(ctx, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, fmt.Sprintf("%s/runner/v1/messages?wait=%d", r.c.base, wait), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+as.RunToken)
		req.Header.Set(headerRun, as.RunID)
		resp, err := r.c.http.Do(req)
		if err != nil {
			return &transportError{err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode >= 500 {
				return &transportError{fmt.Errorf("message poll failed: status %d", resp.StatusCode)}
			}
			return protocol.Errorf(protocol.CodeInvalidRequest, "message poll rejected")
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			return &transportError{fmt.Errorf("decode message page: %w", err)}
		}
		return nil
	})
	return page, err
}

func (r *Runner) deliverRunMessage(ctx context.Context, as protocol.Assignment, w harness.Worker, m protocol.RunMessage, receiver string) error {
	p := protocol.RunMessageDelivery{MessageID: m.MessageID, Receiver: receiver}
	messenger, supported := w.(harness.LiveMessenger)
	if supported {
		p.ThreadID, p.TurnID = messenger.MessageTarget()
	}
	env, err := r.newEnvelope(protocol.OpRunsClaimMessage, runTarget(as), p)
	if err != nil {
		return err
	}
	if err := r.cfg.Journal.SaveMessageIntent(ctx, journal.MessageDelivery{MessageID: m.MessageID, RunID: as.RunID, Claim: *env}); err != nil {
		return err
	}
	op, err := r.send(ctx, runCredential(as), env)
	if op.Status == protocol.OpFailed {
		return r.cfg.Journal.SettleMessageDelivery(ctx, m.MessageID)
	}
	if err != nil {
		return err
	}
	var claimed protocol.RunMessage
	if err := json.Unmarshal(op.Result, &claimed); err != nil {
		return err
	}
	if claimed.Status != protocol.RunMessageDispatching {
		return r.cfg.Journal.SettleMessageDelivery(ctx, m.MessageID)
	}
	if claimed.RunMessageDelivery != p || claimed.TaskID != as.TaskID || claimed.SpecVersion != as.SpecVersion || claimed.Generation != as.Generation || claimed.ActionID != as.ActionID || claimed.RunID != as.RunID {
		return fmt.Errorf("runner: message claim scope mismatch")
	}
	status, reason := protocol.RunMessageUnsupported, "executor_unsupported"
	if supported {
		// Persist before calling the native interface. Even a crash between this
		// write and the call is conservatively unknown, never repeated.
		if err := r.cfg.Journal.MarkMessageInvoked(ctx, m.MessageID); err != nil {
			return err
		}
		nativeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		status, reason = messenger.SendMessage(nativeCtx, harness.SupplementalContext(claimed))
		cancel()
	}
	receipt := protocol.RunMessageReceipt{RunMessageDelivery: p, Status: status, Reason: reason}
	// The worker may have finished while steer was in flight. Preserve the
	// resulting receipt using an independent bounded context before finalizing.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	result, err := r.messageReceiptEnvelope(saveCtx, as, receipt)
	if err != nil {
		return err
	}
	return r.sendMessageReceipt(saveCtx, as, m.MessageID, result)
}

// runMessageLoop has its own lifetime; polling or native rejection never stops
// the worker. Unknown or claimed deliveries block later sends to preserve order.
func (r *Runner) runMessageLoop(ctx context.Context, as protocol.Assignment, w harness.Worker) {
	recoverPending := true
	receiver := r.cfg.IDs.New(protocol.PrefixRequest)
	for ctx.Err() == nil {
		if recoverPending {
			if err := r.recoverRunMessages(ctx, as); err != nil {
				r.cfg.Log.ErrorContext(ctx, "run message recovery remains unresolved", "run_id", as.RunID, "code", protocol.CodeOf(err))
				select {
				case <-ctx.Done():
					return
				case <-time.After(r.cfg.RetryDelay):
				}
				continue
			}
			recoverPending = false
		}
		page, err := r.readPendingMessage(ctx, as, 25)
		if err != nil {
			if ctx.Err() == nil {
				r.cfg.Log.ErrorContext(ctx, "run message polling failed", "run_id", as.RunID, "code", protocol.CodeOf(err))
			}
			return
		}
		if len(page.Messages) == 0 {
			continue
		}
		m := page.Messages[0]
		if m.Status != protocol.RunMessageQueued {
			// A late stored receipt can be uploaded on restart. A native outcome
			// without a receipt must remain unresolved; no resend or bypass.
			return
		}
		if err := r.deliverRunMessage(ctx, as, w, m, receiver); err != nil {
			r.cfg.Log.ErrorContext(ctx, "run message delivery remains unresolved", "run_id", as.RunID, "message_id", m.MessageID, "code", protocol.CodeOf(err))
			// Replay the durable server report before polling another message.
			// Recovery never repeats the native invocation.
			recoverPending = true
		}
	}
}
