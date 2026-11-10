package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"os"
	"path/filepath"
	"time"
)

type externalWaitYield struct{ wait protocol.ExternalWait }

func (*externalWaitYield) Error() string { return "yield execution for external condition" }
func isExternalWaitYield(err error) bool { _, ok := errors.AsType[*externalWaitYield](err); return ok }

func (r *Runner) finishExternalWait(ctx context.Context, as protocol.Assignment) error {
	saved, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	if !saved.StoppedConfirmed {
		return &completionPending{errors.New("external wait awaits physical stop proof")}
	}
	v, err := r.cfg.Journal.ExternalWait(ctx, as.RunID)
	if err != nil {
		return err
	}
	if v == nil {
		return errors.New("external wait receipt is unavailable")
	}
	env, err := r.newEnvelope(protocol.OpRunsYieldExternalWait, runTarget(as), protocol.YieldExternalWaitPayload{WaitID: v.WaitID, ThreadID: saved.Thread})
	if err != nil {
		return err
	}
	if err := r.cfg.Journal.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
		return err
	}
	return r.reportExternalWait(ctx, as)
}

func (r *Runner) reportExternalWait(ctx context.Context, as protocol.Assignment) error {
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	var receipt *journal.Request
	for i := range requests {
		if requests[i].RunID == as.RunID && requests[i].Envelope.Operation == protocol.OpRunsYieldExternalWait {
			receipt = &requests[i]
			break
		}
	}
	if receipt == nil {
		return errors.New("original external wait receipt is unavailable")
	}
	if as.Manager != nil {
		err = r.flushManagerEvents(ctx, as)
	} else {
		err = r.flushEvents(ctx, as)
	}
	if err != nil {
		return &completionPending{err}
	}
	op, err := r.send(ctx, runCredential(as), &receipt.Envelope)
	if err != nil && as.Manager != nil && protocol.CodeOf(err) == protocol.CodeForbidden {
		// A successful native close may have already settled this wait and
		// released the lease before its response reached the runner. Reconcile
		// that original close receipt; never reopen the process or broaden the
		// expired credential to admit a new terminal operation.
		if closed, replayErr := r.replayManagerClose(ctx, as); closed {
			if replayErr != nil {
				return &completionPending{replayErr}
			}
			return nil
		}
	}
	if err != nil || op.Status != protocol.OpSucceeded {
		return &completionPending{errors.Join(err, errors.New("external wait receipt awaits reconciliation"))}
	}
	if as.Manager != nil {
		for _, p := range requests {
			if p.RunID == as.RunID && p.Envelope.Operation == protocol.OpManagerNativeReply {
				if err := r.sendManagerReceipt(ctx, as, &p.Envelope); err != nil {
					return &completionPending{err}
				}
				if err := r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
					return err
				}
			}
		}
	}
	if err := r.recoverRunMessages(ctx, as); err != nil {
		return &completionPending{err}
	}
	return r.finishJournal(ctx, as.RunID)
}
func (r *Runner) externalWaitReceiptReady(ctx context.Context, run journal.Run) (bool, error) {
	if run.Phase != journal.PhaseReleasing || !run.StoppedConfirmed || run.Held {
		return false, nil
	}
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range requests {
		if p.RunID == run.Assignment.RunID && p.Envelope.Operation == protocol.OpRunsYieldExternalWait {
			return true, nil
		}
	}
	return false, nil
}

// externalWaitRequest reuses the durable request/definitive-rejection machinery
// used by worker questions. Receipt loss cannot create another wait identity.
func (r *Runner) externalWaitRequest(ctx context.Context, as protocol.Assignment, p Prepared, payload protocol.WaitExternalPayload) (protocol.ExternalWait, error) {
	q := &ownerQuestions{r: r, as: as, prepared: p}
	env, err := r.persistExternalWaitIntent(p.StateDir, as, payload)
	if err != nil {
		return protocol.ExternalWait{}, err
	}
	op, err := q.submit(ctx, env)
	if err != nil {
		return protocol.ExternalWait{}, err
	}
	var v protocol.ExternalWait
	if err := json.Unmarshal(op.Result, &v); err != nil {
		return v, err
	}
	if err := r.cfg.Journal.RecordExternalWait(context.WithoutCancel(ctx), as.RunID, &v); err != nil {
		return v, err
	}
	return v, nil
}

func (r *Runner) persistExternalWaitIntent(state string, as protocol.Assignment, p protocol.WaitExternalPayload) (*protocol.Envelope, error) {
	if state == "" {
		return nil, errors.New("external wait requires persistent execution state")
	}
	dir := filepath.Join(state, "external-waits")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(state, "owner-questions"), 0700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(p.WaitKey))
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	prior, err := os.ReadFile(path)
	if err == nil {
		var env protocol.Envelope
		if err := json.Unmarshal(prior, &env); err != nil {
			return nil, err
		}
		if env.Operation != protocol.OpRunsWaitExternal || !bytes.Equal(env.Payload, body) {
			return nil, errors.New("wait_key already has different content")
		}
		return &env, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	env, err := r.newEnvelope(protocol.OpRunsWaitExternal, runTarget(as), p)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if err := replaceUploadState(path, func(f *os.File) error { _, err := f.Write(raw); return err }); err != nil {
		return nil, err
	}
	return env, nil
}
func (r *Runner) recoverExternalWait(ctx context.Context, as protocol.Assignment, p Prepared) (*protocol.ExternalWait, error) {
	if saved, err := r.cfg.Journal.ExternalWait(ctx, as.RunID); err != nil || saved != nil {
		return saved, err
	}
	files, err := os.ReadDir(filepath.Join(p.StateDir, "external-waits"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p.StateDir, "external-waits", f.Name()))
		if err != nil {
			return nil, err
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		if env.Operation != protocol.OpRunsWaitExternal {
			return nil, errors.New("external wait intent operation changed")
		}
		q := &ownerQuestions{r: r, as: as, prepared: p}
		op, err := q.submit(ctx, &env)
		if op.Status == protocol.OpFailed {
			continue
		}
		if err != nil {
			return nil, err
		}
		var v protocol.ExternalWait
		if err := json.Unmarshal(op.Result, &v); err != nil {
			return nil, err
		}
		if err := r.cfg.Journal.RecordExternalWait(ctx, as.RunID, &v); err != nil {
			return nil, err
		}
		return &v, nil
	}
	return nil, nil
}

func (r *Runner) reportSessionStop(ctx context.Context, as protocol.Assignment) error {
	if as.Protocol < 3 || !as.SessionStopReports {
		return nil
	}
	saved, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	if saved.Thread == "" {
		return nil
	}
	if !saved.StoppedConfirmed {
		return &completionPending{errors.New("session close awaits physical stop proof")}
	}
	env, err := r.cfg.Journal.SessionStopReport(ctx, as.RunID)
	if err != nil {
		return err
	}
	if env == nil {
		env, err = r.newEnvelope(protocol.OpRunsRecordSessionStop, runTarget(as), protocol.SessionStopPayload{ThreadID: saved.Thread})
		if err != nil {
			return err
		}
		if err := r.cfg.Journal.SaveSessionStopReport(ctx, as.RunID, env); err != nil {
			return err
		}
		env, err = r.cfg.Journal.SessionStopReport(ctx, as.RunID)
		if err != nil {
			return err
		}
	}
	// Reuse metadata must not hold the final business receipt or an independent
	// host Stop behind transport retries. Preserve its exact envelope separately
	// from pending business operations; a fresh session remains a normal path.
	attempt, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if a := r.lookup(as.RunID); a != nil && a.ctx != nil && a.ctx.Err() == nil {
		stop := context.AfterFunc(a.ctx, cancel)
		defer stop()
	}
	_, err = r.sendOnce(attempt, runCredential(as), env)
	if err != nil {
		r.cfg.Log.WarnContext(ctx, "optional session stop report remains uncertain", "run_id", as.RunID, "request_id", env.RequestID, "operation", env.Operation, "err", err)
	}
	return err
}
