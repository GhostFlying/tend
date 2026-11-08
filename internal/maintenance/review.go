package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

// NativeReviewer runs the harness's independent review mode inline, after the
// repair turn has ended. It creates neither a second thread nor an agent tool.
type NativeReviewer interface {
	Review(context.Context, protocol.MaintenanceBackend, string, string) (string, error)
}

func (c *Controller) startReview(ctx context.Context) error {
	reviewer, ok := c.opts.Native.(NativeReviewer)
	if !ok {
		return errors.New("maintenance: native independent review is unavailable")
	}
	a := c.state.Active
	review := c.state.Review
	head, err := c.opts.Head(ctx, c.cfg.Workspace)
	if err != nil {
		return fmt.Errorf("maintenance: read review candidate: %w", err)
	}
	if head != review.Head {
		review.Phase = "invalidated"
		review.Outcome = "head_changed"
		c.enqueue("review candidate changed; prepare and request a fresh review for the exact head")
		return c.save(ctx)
	}
	a.InputID = review.RequestID
	a.Phase = "review-sending"
	review.Phase = "running"
	c.state.Pending = slices.DeleteFunc(c.state.Pending, func(reason string) bool { return reason == "independent review requested" })
	a.Prompt = fmt.Sprintf("Independent read-only review, request_id=%s. Review the configured workspace diff against base %q at exact HEAD %s. Verify HEAD before reviewing; if it differs, report that the requested head was not reviewed. Follow the repository review requirements. Do not edit, merge, deploy, start another agent or start another session. Report actionable findings and their evidence using the native review result.", review.RequestID, review.Base, review.Head)
	if err := c.save(ctx); err != nil {
		return err
	}
	id, err := reviewer.Review(ctx, a.Backend, a.SessionID, a.Prompt)
	if err != nil {
		if errors.Is(err, ErrNotSent) {
			a.InputID = ""
			a.Prompt = ""
			a.Phase = "idle"
			review.Phase = "queued"
			if e := c.save(ctx); e != nil {
				return e
			}
		}
		return err
	}
	a.TurnID = id
	a.Phase = "running"
	return c.save(ctx)
}

func (n *NativeClient) Review(ctx context.Context, b protocol.MaintenanceBackend, id, instructions string) (turnID string, err error) {
	r, err := n.threadRPC(ctx, b, id)
	if err != nil {
		return "", err
	}
	defer func() { n.releaseRPC(id, r, err) }()
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
		ThreadID string `json:"reviewThreadId"`
	}
	err = r.call(ctx, "review/start", map[string]any{"threadId": id, "delivery": "inline", "target": map[string]string{"type": "custom", "instructions": instructions}}, &result)
	if err != nil {
		return "", err
	}
	if result.ThreadID != id {
		return "", errors.New("maintenance: native review did not retain the original thread")
	}
	return result.Turn.ID, nil
}
