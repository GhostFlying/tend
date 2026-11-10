package store

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
)

type PreparedNativeRollout struct {
	Input                         protocol.NativeRolloutChunk
	Host                          string
	Window                        blobstore.Window
	PreviousNext, PreviousIndexed int64
	Indexed                       int64
	Originals                     []preparedOriginal
	Replay                        bool
	Binding                       bool
}

// PrepareNativeRollout verifies ownership and publishes one original chunk
// before taking the writer. It indexes complete records on the same descriptor,
// retaining incomplete JSONL bytes durably for the next upload and backups.
func (s *Store) PrepareNativeRollout(ctx context.Context, host string, p protocol.NativeRolloutChunk) (*PreparedNativeRollout, error) {
	if host == "" || p.RunID == "" || p.SourceID == "" || len(p.SourceID) > 512 || strings.ContainsAny(p.SourceID, "\x00\r\n") || p.ThreadID == "" || len(p.ThreadID) > 512 || strings.ContainsAny(p.ThreadID, "\x00\r\n") || p.Offset < 0 || len(p.Data) > protocol.MaxNativeRolloutChunkBytes || bodyHash(p.Data) != p.SHA256 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid native original chunk identity, digest, offset or size")
	}
	prepared := &PreparedNativeRollout{Host: host, Input: p}
	var known bool
	var thread, archive string
	var chunks []blobstore.Window
	err := s.Read(ctx, func(tx *ReadTx) error {
		run, err := GetRun(ctx, tx.Q(), p.RunID)
		if err != nil {
			return err
		}
		if run.HostID != host {
			return protocol.Errorf(protocol.CodeForbidden, "native original requires its own host execution")
		}
		err = tx.Q().QueryRowContext(ctx, `SELECT thread_id,archive_id,next_offset,indexed_offset FROM retained_native_sources WHERE host_id=? AND source_id=?`, host, p.SourceID).Scan(&thread, &archive, &prepared.PreviousNext, &prepared.PreviousIndexed)
		if err == nil {
			known = true
			if thread != p.ThreadID {
				return protocol.Errorf(protocol.CodeConflictPayload, "native original generation has another thread identity")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if len(p.Data) == 0 {
			if !known || prepared.PreviousNext != p.Offset {
				return protocol.Errorf(protocol.CodeStaleRevision, "native original binding requires its current retained offset")
			}
			prepared.Replay, prepared.Binding = true, true
			prepared.Indexed = prepared.PreviousIndexed
			return nil
		}
		if prepared.PreviousNext > p.Offset {
			var length, indexed int64
			var hash string
			err := tx.Q().QueryRowContext(ctx, `SELECT byte_length,sha256,indexed_offset FROM retained_native_chunks WHERE host_id=? AND source_id=? AND byte_offset=?`, host, p.SourceID, p.Offset).Scan(&length, &hash, &indexed)
			if err != nil {
				return err
			}
			if length != int64(len(p.Data)) || hash != p.SHA256 {
				return protocol.Errorf(protocol.CodeConflictPayload, "native original chunk has different retained bytes")
			}
			prepared.Replay = true
			prepared.Indexed = indexed
			return nil
		}
		if prepared.PreviousNext != p.Offset {
			return protocol.Errorf(protocol.CodeStaleRevision, "native original next offset is %d", prepared.PreviousNext)
		}
		if known {
			rows, err := tx.Q().QueryContext(ctx, `SELECT byte_offset,byte_length,sha256 FROM retained_native_chunks WHERE host_id=? AND source_id=? AND byte_offset+byte_length>? ORDER BY byte_offset`, host, p.SourceID, prepared.PreviousIndexed)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				w := blobstore.Window{Archive: archive}
				if err = rows.Scan(&w.Offset, &w.Length, &w.SHA256); err != nil {
					return err
				}
				chunks = append(chunks, w)
			}
			return rows.Err()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if prepared.Replay {
		prepared.Window = blobstore.Window{Archive: archive, Offset: p.Offset, Length: int64(len(p.Data)), SHA256: p.SHA256}
		if !prepared.Binding {
			if _, err = s.files.ReadWindow(ctx, prepared.Window); err != nil {
				return nil, err
			}
		}
		return prepared, nil
	}
	prepared.Window, err = s.files.AppendOriginalAt(ctx, "native-rollout:"+host+":"+p.SourceID, p.Offset, p.Data)
	if err != nil {
		return nil, err
	}
	if known && archive != prepared.Window.Archive {
		return nil, errors.New("store: native original archive identity differs")
	}
	for _, w := range chunks {
		if _, err = s.files.ReadWindow(ctx, w); err != nil {
			return nil, err
		}
	}
	if _, err = s.files.ReadWindow(ctx, prepared.Window); err != nil {
		return nil, err
	}
	next := p.Offset + int64(len(p.Data))
	reader, err := s.files.OpenOriginalRange(prepared.Window.Archive, prepared.PreviousIndexed, next-prepared.PreviousIndexed)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	buffered := bufio.NewReader(contextReaderStore{ctx, reader})
	offset := prepared.PreviousIndexed
	prepared.Indexed = offset
	for offset < next {
		start := offset
		var record []byte
		oversized := false
		for {
			part, readErr := buffered.ReadSlice('\n')
			offset += int64(len(part))
			if !oversized && len(record)+len(part) <= 16<<20 {
				record = append(record, part...)
			} else {
				oversized = true
				record = nil
			}
			if errors.Is(readErr, bufio.ErrBufferFull) {
				continue
			}
			if readErr == io.EOF {
				return prepared, nil
			}
			if readErr != nil {
				return nil, readErr
			}
			break
		}
		if !oversized {
			trimmed := bytes.TrimSpace(record)
			if len(trimmed) > 0 {
				if !jsonValidOriginal(trimmed) {
					return nil, protocol.Errorf(protocol.CodeInvalidRequest, "native original has invalid JSON at byte %d", start)
				}
				batch := OriginalImportBatch{whole: preparedOriginal{Window: prepared.Window}}
				indexOriginalJSON(&batch, trimmed, start+int64(bytes.Index(record, trimmed)), 0)
				prepared.Originals = append(prepared.Originals, batch.originals...)
			}
		}
		// Oversized lines remain in their original chunks. The preview reader has
		// the same 16 MiB indexing bound; later complete records still make progress.
		prepared.Indexed = offset
	}
	return prepared, nil
}
func jsonValidOriginal(raw []byte) bool { return json.Valid(raw) }

func (t *Tx) ApplyNativeRollout(ctx context.Context, p *PreparedNativeRollout) (protocol.NativeRolloutArchiveReceipt, error) {
	var receipt protocol.NativeRolloutArchiveReceipt
	if p == nil {
		return receipt, errors.New("store: native original was not staged")
	}
	receipt = protocol.NativeRolloutArchiveReceipt{SourceID: p.Input.SourceID, Offset: p.Input.Offset, NextOffset: p.Input.Offset + int64(len(p.Input.Data)), SHA256: p.Input.SHA256, IndexedOffset: p.Indexed}
	if p.Replay {
		_, err := t.Exec(ctx, `INSERT OR IGNORE INTO retained_native_source_runs(host_id,source_id,run_id) VALUES(?,?,?)`, p.Host, p.Input.SourceID, p.Input.RunID)
		return receipt, err
	}
	var current int64
	if err := t.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT next_offset FROM retained_native_sources WHERE host_id=? AND source_id=?),0)`, p.Host, p.Input.SourceID).Scan(&current); err != nil {
		return receipt, err
	}
	if current != p.PreviousNext {
		return receipt, protocol.Errorf(protocol.CodeStaleRevision, "native original advanced while staging")
	}
	if _, err := t.Exec(ctx, `INSERT INTO retained_native_sources(host_id,source_id,thread_id,archive_id,next_offset,indexed_offset) VALUES(?,?,?,?,?,?) ON CONFLICT(host_id,source_id) DO UPDATE SET next_offset=excluded.next_offset,indexed_offset=excluded.indexed_offset`, p.Host, p.Input.SourceID, p.Input.ThreadID, p.Window.Archive, receipt.NextOffset, p.Indexed); err != nil {
		return receipt, err
	}
	if _, err := t.Exec(ctx, `INSERT INTO retained_native_chunks(host_id,source_id,byte_offset,byte_length,sha256,indexed_offset) VALUES(?,?,?,?,?,?)`, p.Host, p.Input.SourceID, p.Window.Offset, p.Window.Length, p.Window.SHA256, p.Indexed); err != nil {
		return receipt, err
	}
	// Every accepted chunk is a backup dependency, including an unindexed tail.
	windows := append([]preparedOriginal{{Window: p.Window, Characters: int64(utf8.RuneCount(p.Input.Data))}}, p.Originals...)
	for _, original := range windows {
		if _, err := t.bindOriginalWindow(ctx, original); err != nil {
			return receipt, err
		}
	}
	_, err := t.Exec(ctx, `INSERT OR IGNORE INTO retained_native_source_runs(host_id,source_id,run_id) VALUES(?,?,?)`, p.Host, p.Input.SourceID, p.Input.RunID)
	return receipt, err
}
