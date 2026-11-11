package store

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
)

type OriginalImportBatch struct {
	input     protocol.OriginalImport
	originals []preparedOriginal
	next      int64
	more      bool
	records   int
	whole     preparedOriginal
	manifest  protocol.ConversationAttachment
}

// PrepareOriginalImport indexes a complete uploaded rollout in place. The
// original is the existing blobstore object: no per-record files are created.
func (s *Store) PrepareOriginalImport(ctx context.Context, p protocol.OriginalImport) (*OriginalImportBatch, error) {
	if p.SourceKey == "" || len(p.SourceKey) > 512 || p.After < 0 || p.Limit < 1 || p.Limit > 1000 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "import requires source_key, file_id, a record-boundary cursor and limit 1..1000")
	}
	var manifest ConversationAttachmentRow
	if err := s.Read(ctx, func(tx *ReadTx) error {
		var e error
		manifest, e = GetConversationAttachment(ctx, tx.Q(), p.FileID)
		return e
	}); err != nil {
		return nil, err
	}
	f, err := s.files.Open(manifest.SHA256)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, contextReaderStore{ctx, f})
	if err != nil {
		return nil, err
	}
	if n != manifest.Size || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return nil, errors.New("store: rollout original differs from uploaded manifest")
	}
	if p.After > n {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "import cursor exceeds original")
	}
	if p.After > 0 && p.After < n {
		var prior [1]byte
		if _, err = f.ReadAt(prior[:], p.After-1); err != nil {
			return nil, err
		}
		if prior[0] != '\n' {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "import cursor must follow a complete JSONL record")
		}
	}
	if _, err = f.Seek(p.After, io.SeekStart); err != nil {
		return nil, err
	}
	batch := &OriginalImportBatch{input: p, next: p.After, manifest: manifest.ConversationAttachment, whole: preparedOriginal{Window: blobstore.Window{Archive: "object:" + manifest.SHA256, Offset: 0, Length: n, SHA256: manifest.SHA256}}}
	reader := bufio.NewReader(f)
	for batch.records < p.Limit && batch.next < n {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		start := batch.next
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		batch.next += int64(len(line))
		trim := bytes.TrimSpace(line)
		if len(trim) == 0 {
			continue
		}
		if !json.Valid(trim) {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "rollout contains invalid JSON at byte %d", start)
		}
		leading := bytes.Index(line, trim)
		indexOriginalJSON(batch, trim, start+int64(leading), 0)
		batch.records++
	}
	batch.more = batch.next < n
	return batch, nil
}

type contextReaderStore struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReaderStore) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}

func indexOriginalJSON(batch *OriginalImportBatch, raw []byte, offset int64, depth int) {
	if len(raw) < retainBodyThreshold || depth > 6 {
		return
	}
	batch.originals = append(batch.originals, preparedOriginal{Window: blobstore.Window{Archive: batch.whole.Window.Archive, Offset: offset, Length: int64(len(raw)), SHA256: bodyHash(raw)}, Characters: int64(utf8.RuneCount(raw)), CanonicalSHA256: canonicalOriginalHash(raw)})
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return
	}
	delim, ok := token.(json.Delim)
	if !ok || (delim != '{' && delim != '[') {
		return
	}
	for dec.More() {
		if delim == '{' {
			if _, err = dec.Token(); err != nil {
				return
			}
		}
		start := int(dec.InputOffset())
		for start < len(raw) && strings.ContainsRune(" \r\n\t:,", rune(raw[start])) {
			start++
		}
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return
		}
		indexOriginalJSON(batch, value, offset+int64(start), depth+1)
	}
}

func (t *Tx) ApplyOriginalImport(ctx context.Context, batch *OriginalImportBatch, revision, now int64) (protocol.OriginalImportResult, error) {
	var result protocol.OriginalImportResult
	if batch == nil {
		return result, errors.New("store: rollout import was not prepared")
	}
	var current int64
	if err := t.Q().QueryRowContext(ctx, `SELECT revision FROM retained_body_migration WHERE singleton=1`).Scan(&current); err != nil {
		return result, err
	}
	if current != revision {
		return result, protocol.Errorf(protocol.CodeStaleRevision, "original storage revision is %d", current)
	}
	var imported int64
	err := t.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT indexed_until FROM retained_rollout_archives WHERE source_key=? AND sha256=?),0)`, batch.input.SourceKey, batch.manifest.SHA256).Scan(&imported)
	if err != nil {
		return result, err
	}
	if imported != batch.input.After {
		return result, protocol.Errorf(protocol.CodeStaleRevision, "rollout import cursor is %d", imported)
	}
	// The entire original is a backup dependency even when only a prefix has been
	// indexed; metadata checkpoints do not declare unindexed history collected.
	all := append([]preparedOriginal{batch.whole}, batch.originals...)
	for _, p := range all {
		if _, err = t.bindOriginalWindow(ctx, p); err != nil {
			return result, err
		}
	}
	if _, err = t.Exec(ctx, `INSERT INTO retained_rollout_archives(source_key,sha256,file_id,indexed_until) VALUES(?,?,?,?) ON CONFLICT(source_key,sha256) DO UPDATE SET indexed_until=excluded.indexed_until`, batch.input.SourceKey, batch.manifest.SHA256, batch.input.FileID, batch.next); err != nil {
		return result, err
	}
	if _, err = t.Exec(ctx, `UPDATE retained_body_migration SET revision=revision+1,updated_at=? WHERE singleton=1`, now); err != nil {
		return result, err
	}
	result.Status, err = originalStorageStatus(ctx, t.Q(), false)
	result.FileID = batch.input.FileID
	result.SourceKey = batch.input.SourceKey
	result.IndexedRecords = batch.records
	result.IndexedBodies = len(batch.originals)
	result.Next = batch.next
	result.More = batch.more
	return result, err
}
