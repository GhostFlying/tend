package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) historyReplies() []protocol.ManagerHistoryReply {
	r.mu.Lock()
	defer r.mu.Unlock()
	replies := make([]protocol.ManagerHistoryReply, 0, len(r.historyResults))
	for _, reply := range r.historyResults {
		replies = append(replies, reply)
	}
	return replies
}

func (r *Runner) acceptHistoryReads(ctx context.Context, reads []protocol.ManagerHistoryRead, acknowledged []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range acknowledged {
		delete(r.historyResults, id)
	}
	if r.historyReading == nil {
		r.historyReading = map[string]bool{}
		r.historyResults = map[string]protocol.ManagerHistoryReply{}
	}
	for _, read := range reads {
		if read.ID == "" || r.historyReading[read.ID] || len(r.historyReading) >= 4 || len(r.historyResults) >= 128 {
			continue
		}
		if _, done := r.historyResults[read.ID]; done {
			continue
		}
		r.historyReading[read.ID] = true
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			readCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
			defer cancel()
			result, err := r.readManagerHistory(readCtx, read)
			reply := protocol.ManagerHistoryReply{ID: read.ID, Result: result}
			if err != nil {
				reply.Error = "original native history is unavailable"
				r.cfg.Log.WarnContext(ctx, "manager history read failed", "run_id", read.RunID, "err", err)
			}
			r.mu.Lock()
			delete(r.historyReading, read.ID)
			r.historyResults[read.ID] = reply
			r.mu.Unlock()
			r.requestHeartbeat()
		}()
	}
}

func (r *Runner) readManagerHistory(ctx context.Context, read protocol.ManagerHistoryRead) (json.RawMessage, error) {
	if read.Execution.Validate(r.cfg.HostID) != nil || read.ThreadID == "" || read.MessageID == "" {
		return nil, errors.New("runner: invalid original history identity")
	}
	resolver, ok := r.cfg.Executions.(ManagerReceiptExecutionResolver)
	if !ok {
		return nil, errors.New("runner: native history reader unavailable")
	}
	resolved, err := resolver.ResolveManagerReceipt(ctx, read.Execution)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(read.ConversationID))
	root := r.cfg.Journal.Directory()
	if read.RetiredJournal != nil {
		root, err = r.retiredJournalDir(*read.RetiredJournal)
		if err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(root, "manager-sessions", hex.EncodeToString(key[:]))
	if read.RetiredJournal != nil {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("original manager history directory missing")
		}
	} else if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	session, err := resolved.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	history, ok := session.(harness.ThreadHistoryReader)
	if !ok {
		return nil, errors.New("runner: executor does not support non-executing history reads")
	}
	thread, err := history.ReadThreadHistory(ctx, read.ThreadID)
	if err != nil {
		return nil, err
	}
	finder, ok := thread.(harness.TurnReader)
	if !ok {
		return nil, errors.New("runner: executor has no native receipt reader")
	}
	receipt, found, err := finder.FindTurn(ctx, read.MessageID, read.TurnID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(coordination.FindResult{Receipt: receipt, Found: found})
}
