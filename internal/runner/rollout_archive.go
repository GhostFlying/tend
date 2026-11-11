package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/harness/rollout"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

const rolloutArchiveChunkBytes = protocol.MaxNativeRolloutChunkBytes

// syncRolloutArchive performs one bounded transport attempt, independently of
// event or business delivery. Only acknowledged bytes advance the saved cursor.
func (r *Runner) syncRolloutArchive(ctx context.Context, as protocol.Assignment) error {
	r.controlMu.Lock()
	deployment, version := r.previewDeployment, r.runnerProtocol
	r.controlMu.Unlock()
	if as.Protocol < 3 || version < 3 || deployment == "" || len(r.cfg.RolloutRoots) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	sources, err := r.cfg.Journal.RolloutSources(ctx, deployment, as.RunID)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return errors.New("runner: rollout archive has no registered source for this deployment")
	}
	seen := map[string]bool{}
	for _, source := range sources {
		if seen[source.SessionID] {
			continue
		}
		seen[source.SessionID] = true
		if err := r.syncRolloutSource(ctx, deployment, as, source); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) syncRolloutSource(ctx context.Context, deployment string, as protocol.Assignment, source protocol.RolloutPreviewSource) error {
	unlock, err := r.lockArchiveSource(ctx, deployment+"/"+source.SessionID)
	if err != nil {
		return err
	}
	defer unlock()
	saved, err := r.cfg.Journal.RolloutArchive(ctx, deployment, source.SessionID)
	if err != nil {
		return err
	}
	// Reconcile the exact saved bytes even if the native path has since rotated
	// or disappeared. No new request may replace an uncertain original upload.
	if saved.Pending != nil {
		if err := r.sendRolloutArchive(ctx, deployment, source.SessionID, *saved.Pending); err != nil {
			return err
		}
		saved.Cursor = saved.Next
	}
	if source.Path == "" {
		source.Path, err = rollout.Locate(ctx, r.cfg.RolloutRoots, source.SessionID)
		if err != nil {
			return err
		}
		if source.Path == "" {
			// Missing originals stay pending; EOF was not observed.
			return errors.New("runner: rollout archive original is not yet available")
		}
		if err := r.registerRollout(ctx, as.RunID, source); err != nil {
			return err
		}
	}
	f, err := rollout.Open(r.cfg.RolloutRoots, source.Path)
	if err != nil {
		return fmt.Errorf("runner: open rollout archive original: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("runner: rollout archive original is not a regular file")
	}
	var header struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&header); err != nil || header.Type != "session_meta" || header.Payload.ID != source.SessionID {
		return errors.New("runner: rollout archive native identity differs from registered source")
	}
	current := saved.Cursor
	if !archiveGenerationMatches(f, info, current) {
		current = journal.RolloutArchiveCursor{SourceID: r.cfg.IDs.New("rollout"), FileID: nativeusage.FileIdentity(f, info)}
	}
	// One flush snapshots a finite native tail. Continuous native output cannot
	// keep this uploader chasing EOF indefinitely; each wire call is also bounded.
	end := info.Size()
	for current.Offset < end {
		if err := ctx.Err(); err != nil {
			return err
		}
		data := make([]byte, min(int64(rolloutArchiveChunkBytes), end-current.Offset))
		if _, err := f.ReadAt(data, current.Offset); err != nil {
			return fmt.Errorf("runner: read rollout archive original: %w", err)
		}
		digest := sha256.Sum256(data)
		chunk := protocol.NativeRolloutChunk{RunID: as.RunID, SourceID: current.SourceID, ThreadID: source.SessionID, Offset: current.Offset, Data: data, SHA256: hex.EncodeToString(digest[:])}
		next := current
		next.Offset += int64(len(data))
		next.LastRunID = as.RunID
		next.PrefixSize = min(next.Offset, int64(64<<10))
		next.PrefixHash, err = archiveWindowHash(f, 0, next.PrefixSize)
		if err != nil {
			return err
		}
		next.AnchorHash, err = archiveWindowHash(f, max(0, next.Offset-64<<10), min(next.Offset, int64(64<<10)))
		if err != nil {
			return err
		}
		env, err := r.newEnvelope(protocol.OpRunnerRolloutArchive, protocol.Target{Type: "run", ID: as.RunID}, chunk)
		if err != nil {
			return err
		}
		if err := r.cfg.Journal.QueueRolloutArchive(ctx, deployment, source.SessionID, saved.Cursor, next, *env); err != nil {
			return err
		}
		if err := r.sendRolloutArchive(ctx, deployment, source.SessionID, *env); err != nil {
			return err
		}
		current, saved.Cursor = next, next
	}
	if current.Offset > 0 && current.LastRunID != as.RunID {
		// A resumed thread can already be fully archived by another Run. Bind
		// its index without copying its original prefix or inventing a new source.
		next := current
		next.LastRunID = as.RunID
		digest := sha256.Sum256(nil)
		chunk := protocol.NativeRolloutChunk{RunID: as.RunID, SourceID: current.SourceID, ThreadID: source.SessionID, Offset: current.Offset, Data: []byte{}, SHA256: hex.EncodeToString(digest[:])}
		env, err := r.newEnvelope(protocol.OpRunnerRolloutArchive, protocol.Target{Type: "run", ID: as.RunID}, chunk)
		if err != nil {
			return err
		}
		if err := r.cfg.Journal.QueueRolloutArchive(ctx, deployment, source.SessionID, saved.Cursor, next, *env); err != nil {
			return err
		}
		return r.sendRolloutArchive(ctx, deployment, source.SessionID, *env)
	}
	return nil
}

func archiveGenerationMatches(f *os.File, info os.FileInfo, cursor journal.RolloutArchiveCursor) bool {
	if cursor.SourceID == "" || cursor.Offset < 0 || info.Size() < cursor.Offset || cursor.FileID != "" && cursor.FileID != nativeusage.FileIdentity(f, info) {
		return false
	}
	prefix, err := archiveWindowHash(f, 0, cursor.PrefixSize)
	if err != nil || prefix != cursor.PrefixHash {
		return false
	}
	anchor, err := archiveWindowHash(f, max(0, cursor.Offset-64<<10), min(cursor.Offset, int64(64<<10)))
	return err == nil && anchor == cursor.AnchorHash
}

func archiveWindowHash(f *os.File, offset, length int64) (string, error) {
	hash := sha256.New()
	if _, err := io.CopyN(hash, io.NewSectionReader(f, offset, length), length); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (r *Runner) sendRolloutArchive(ctx context.Context, deployment, thread string, env protocol.Envelope) error {
	op, err := r.sendOnce(ctx, r.c.host(), &env)
	if err != nil {
		return err
	}
	if op.Status != protocol.OpSucceeded {
		return errors.New("runner: rollout archive durable receipt pending")
	}
	var receipt protocol.NativeRolloutArchiveReceipt
	if err := json.Unmarshal(op.Result, &receipt); err != nil {
		return fmt.Errorf("runner: decode rollout archive receipt: %w", err)
	}
	return r.cfg.Journal.AcknowledgeRolloutArchive(ctx, deployment, thread, env.RequestID, receipt)
}
