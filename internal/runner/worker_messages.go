package runner

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// workerMessages offers context through the original run's authenticated MCP
// bridge. Only a later ack_message call proves executor receipt; producing a
// tool response alone leaves dispatching. Each offer is durably recorded before
// returning text and is never offered again after loss or restart.
type workerMessages struct {
	r         *Runner
	as        protocol.Assignment
	receiver  string
	terminal  string
	mu        sync.Mutex
	recovered bool
}

func (m *workerMessages) receive(ctx context.Context) (*protocol.RunMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.recovered {
		if err := m.r.recoverRunMessages(ctx, m.as); err != nil {
			return nil, err
		}
		m.recovered = true
	}
	// A tool caller can continue in the same process after a timed-out claim
	// or acknowledgement. Flush saved proof without treating the live receiver
	// as a restart; an unacknowledged offered body must stay unresolved.
	pending, err := m.r.cfg.Journal.PendingMessageDeliveries(ctx, m.as.RunID)
	if err != nil {
		return nil, err
	}
	for _, d := range pending {
		if d.Report != nil {
			if err := m.r.sendMessageReceipt(ctx, m.as, d.MessageID, d.Report); err != nil {
				return nil, err
			}
		}
	}
	page, err := m.r.readPendingMessage(ctx, m.as, 0)
	if err != nil || len(page.Messages) == 0 {
		return nil, err
	}
	msg := page.Messages[0]
	var saved *protocol.Envelope
	for _, d := range pending {
		if d.MessageID == msg.MessageID && !d.Invoked && d.Report == nil {
			saved = &d.Claim
			break
		}
	}
	if msg.Status != protocol.RunMessageQueued && saved == nil {
		msg.Text = ""
		return &msg, nil
	}
	run, err := m.r.cfg.Journal.Run(ctx, m.as.RunID)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil || run.Held || run.Fenced || run.Phase != journal.PhaseStarted {
		return nil, errors.New("worker messages are unavailable under current control")
	}
	var native struct {
		SessionID string `json:"session_id"`
		Terminal  string `json:"terminal"`
	}
	if err := json.Unmarshal([]byte(run.Thread), &native); err != nil || native.Terminal == "" {
		return nil, errors.New("original worker terminal identity is unavailable")
	}
	// A terminal can replace its native session in place. Wait for a stable
	// provider identity rather than claiming against the terminal placeholder.
	if native.SessionID == "" {
		return nil, nil
	}
	thread := native.SessionID
	p := protocol.RunMessageDelivery{MessageID: msg.MessageID, Receiver: m.receiver, ThreadID: thread}
	env := saved
	if env != nil {
		var original protocol.RunMessageDelivery
		if err := json.Unmarshal(env.Payload, &original); err != nil {
			return nil, err
		}
		if original != p {
			return nil, errors.New("pending claim belongs to another worker incarnation")
		}
	} else {
		env, err = m.r.newEnvelope(protocol.OpRunsClaimMessage, runTarget(m.as), p)
		if err != nil {
			return nil, err
		}
		if err := m.r.cfg.Journal.SaveMessageIntent(ctx, journal.MessageDelivery{MessageID: msg.MessageID, RunID: m.as.RunID, Claim: *env}); err != nil {
			return nil, err
		}
	}
	op, err := m.r.send(ctx, runCredential(m.as), env)
	if op.Status == protocol.OpFailed {
		_ = m.r.cfg.Journal.SettleMessageDelivery(ctx, msg.MessageID)
		return nil, op.Error
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(op.Result, &msg); err != nil {
		return nil, err
	}
	if msg.Status != protocol.RunMessageDispatching {
		return nil, nil
	}
	if msg.RunMessageDelivery != p || msg.RunID != m.as.RunID || msg.TaskID != m.as.TaskID || msg.ActionID != m.as.ActionID || msg.SpecVersion != m.as.SpecVersion || msg.Generation != m.as.Generation {
		return nil, errors.New("worker message scope does not match")
	}
	if err := m.r.cfg.Journal.MarkMessageInvoked(ctx, msg.MessageID); err != nil {
		return nil, err
	}
	m.terminal = native.Terminal
	return &msg, nil
}

func (m *workerMessages) ack(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.r.cfg.Journal.MessageDelivery(ctx, m.as.RunID, id)
	if err != nil {
		return err
	}
	var binding protocol.RunMessageDelivery
	if err := json.Unmarshal(d.Claim.Payload, &binding); err != nil {
		return err
	}
	if binding.Receiver != m.receiver || !d.Invoked {
		return errors.New("message was not offered to this worker incarnation")
	}
	if d.Report == nil {
		run, readErr := m.r.cfg.Journal.Run(ctx, m.as.RunID)
		if readErr != nil {
			return readErr
		}
		var native struct {
			SessionID string `json:"session_id"`
			Terminal  string `json:"terminal"`
		}
		if err := json.Unmarshal([]byte(run.Thread), &native); err != nil {
			return err
		}
		if native.SessionID != binding.ThreadID || native.Terminal != m.terminal || m.terminal == "" {
			// Replacement is not evidence that the original receiver read the body.
			// Retain uncertainty durably; never reoffer the original text.
			report, err := m.r.messageReceiptEnvelope(ctx, m.as, protocol.RunMessageReceipt{RunMessageDelivery: binding, Status: protocol.RunMessageUnknown, Reason: "receiver_restarted"})
			if err != nil {
				return err
			}
			if err := m.r.sendMessageReceipt(ctx, m.as, id, report); err != nil {
				return err
			}
			return errors.New("message belongs to a previous native worker incarnation")
		}
		d.Report, err = m.r.messageReceiptEnvelope(ctx, m.as, protocol.RunMessageReceipt{RunMessageDelivery: binding, Status: protocol.RunMessageAccepted, Reason: "worker_acknowledged"})
		if err != nil {
			return err
		}
	}
	return m.r.sendMessageReceipt(ctx, m.as, id, d.Report)
}
