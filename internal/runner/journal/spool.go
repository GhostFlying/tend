package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
)

const spoolInlineLimit = 8 << 10

type spoolReference struct {
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}
type spoolBody struct {
	Reference spoolReference `json:"tend_spool"`
}

func (j *Journal) spoolStore() (*blobstore.Store, error) { return blobstore.New(j.path + ".spool") }

// retainBody publishes complete immutable bytes before committing their journal
// reference. It runs under the journal writer lock, which also fences pruning.
func (j *Journal) retainBody(ctx context.Context, body string) (string, error) {
	var literal map[string]json.RawMessage
	reserved := json.Unmarshal([]byte(body), &literal) == nil && len(literal) == 1 && literal["tend_spool"] != nil
	if len(body) <= spoolInlineLimit && !reserved {
		return body, nil
	}
	s, err := j.spoolStore()
	if err != nil {
		return "", err
	}
	obj, err := s.Put(ctx, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(spoolBody{spoolReference{1, obj.SHA256, obj.Size}})
	return string(raw), err
}

func (j *Journal) readBody(ctx context.Context, body string) (string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &fields) != nil || len(fields) != 1 || fields["tend_spool"] == nil {
		return body, nil
	}
	var ref spoolBody
	if err := json.Unmarshal([]byte(body), &ref); err != nil {
		return "", err
	}
	r := ref.Reference
	if r.Version != 1 || r.Bytes <= 0 {
		return "", errors.New("journal: invalid spool reference")
	}
	s, err := j.spoolStore()
	if err != nil {
		return "", err
	}
	f, err := s.Open(r.SHA256)
	if err != nil {
		return "", fmt.Errorf("journal: missing spool original: %w", err)
	}
	defer f.Close()
	var out bytes.Buffer
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(&out, h), io.LimitReader(&spoolReader{ctx, f}, r.Bytes+1))
	if err != nil {
		return "", err
	}
	if n != r.Bytes || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return "", errors.New("journal: corrupt spool original")
	}
	return out.String(), nil
}

type spoolReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *spoolReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// PruneSpool removes only unreferenced transmission objects. Every textual
// journal column is a root, including retained unknown/Stop snapshots. It never
// visits native CLI rollout paths. Backups retain independent immutable copies.
func (j *Journal) PruneSpool(ctx context.Context) (int64, error) {
	j.spoolMu.Lock()
	defer j.spoolMu.Unlock()
	roots := map[string]bool{}
	rows, err := j.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return 0, err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			break
		}
		tables = append(tables, n)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return 0, err
	}
	for _, table := range tables {
		cols, e := j.db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
		if e != nil {
			return 0, e
		}
		var textCols []string
		for cols.Next() {
			var id, notnull, pk int
			var name, kind string
			var def any
			if e = cols.Scan(&id, &name, &kind, &notnull, &def, &pk); e != nil {
				break
			}
			if strings.EqualFold(kind, "TEXT") {
				textCols = append(textCols, name)
			}
		}
		e = errors.Join(e, cols.Err(), cols.Close())
		if e != nil {
			return 0, e
		}
		for _, col := range textCols {
			values, e := j.db.QueryContext(ctx, `SELECT `+quoteIdentifier(col)+` FROM `+quoteIdentifier(table)+` WHERE instr(`+quoteIdentifier(col)+`, 'tend_spool')>0`)
			if e != nil {
				return 0, e
			}
			for values.Next() {
				var v string
				if e = values.Scan(&v); e != nil {
					break
				}
				collectSpoolReferences(v, roots)
			}
			e = errors.Join(e, values.Err(), values.Close())
			if e != nil {
				return 0, e
			}
		}
	}
	var reclaimed int64
	err = filepath.WalkDir(j.path+".spool", func(path string, d fs.DirEntry, e error) error {
		if errors.Is(e, os.ErrNotExist) && path == j.path+".spool" {
			return nil
		}
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("journal: spool contains symlink")
		}
		if d.IsDir() {
			return nil
		}
		hash := d.Name()
		key, e := hex.DecodeString(hash)
		if e != nil || len(key) != sha256.Size || strings.ToLower(hash) != hash {
			return nil
		}
		if roots[hash] {
			return nil
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("journal: non-regular spool original")
		}
		if e = os.Remove(path); e != nil {
			return e
		}
		reclaimed += info.Size()
		return nil
	})
	return reclaimed, err
}

func collectSpoolReferences(raw string, roots map[string]bool) {
	// Nested recovery snapshots can retain JSON as quoted strings. Content keys
	// are fixed lowercase hex; retaining extra matches is safe and bounded.
	for i := 0; i+64 <= len(raw); i++ {
		key := raw[i : i+64]
		if _, err := hex.DecodeString(key); err == nil && strings.ToLower(key) == key {
			roots[key] = true
			i += 63
		}
	}
}
func quoteIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// MigrateSpool moves a bounded batch of existing inline transmission bodies.
// A failed publication/transaction leaves the inline original intact. Frozen
// historical snapshots remain readable and protected until explicitly settled.
func (j *Journal) MigrateSpool(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("journal: invalid spool migration limit")
	}
	count := 0
	err := j.tx(ctx, func(tx *sql.Tx) error {
		for _, field := range []struct{ table, column string }{{"events", "body_json"}, {"requests", "payload_json"}, {"manager_native_operations", "request_json"}, {"manager_native_operations", "reply_json"}, {"rollout_archive_cursors", "pending_json"}, {"runs", "worker_result_json"}, {"manager_native_attachments", "request_json"}} {
			if count == limit {
				break
			}
			filter := ""
			if field.table == "requests" {
				filter = " AND NOT EXISTS(SELECT 1 FROM bounded_recovery_quarantines WHERE active=1) AND NOT EXISTS(SELECT 1 FROM retained_run_resumes a WHERE a.run_id=requests.run_id)"
			}
			rows, err := tx.QueryContext(ctx, `SELECT rowid,`+quoteIdentifier(field.column)+` FROM `+quoteIdentifier(field.table)+` WHERE length(CAST(`+quoteIdentifier(field.column)+` AS BLOB)) > ?`+filter+` LIMIT ?`, spoolInlineLimit, limit-count)
			if err != nil {
				return err
			}
			type value struct {
				id   int64
				body string
			}
			var batch []value
			for rows.Next() {
				var v value
				if err = rows.Scan(&v.id, &v.body); err != nil {
					break
				}
				batch = append(batch, v)
			}
			err = errors.Join(err, rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			for _, v := range batch {
				// Native operation headers are queried for method/error/close facts. Only
				// their opaque payload/result fields are externalized.
				body := v.body
				if field.table == "manager_native_operations" || field.table == "manager_native_attachments" {
					body, err = j.retainNativeBody(ctx, body, field.column == "request_json")
				} else {
					body, err = j.retainBody(ctx, body)
				}
				if err != nil {
					return err
				}
				if body == v.body {
					continue
				}
				if _, err = tx.ExecContext(ctx, `UPDATE `+quoteIdentifier(field.table)+` SET `+quoteIdentifier(field.column)+`=? WHERE rowid=?`, body, v.id); err != nil {
					return err
				}
				count++
			}
		}
		return nil
	})
	return count, err
}

func (j *Journal) retainNativeBody(ctx context.Context, body string, request bool) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		return "", err
	}
	key := "result"
	if request {
		key = "payload"
	}
	raw := fields[key]
	if len(raw) == 0 {
		return body, nil
	}
	saved, err := j.retainBody(ctx, string(raw))
	if err != nil {
		return "", err
	}
	fields[key] = json.RawMessage(saved)
	out, err := json.Marshal(fields)
	return string(out), err
}
func (j *Journal) readNativeBody(ctx context.Context, body string, request bool) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		return "", err
	}
	key := "result"
	if request {
		key = "payload"
	}
	raw := fields[key]
	if len(raw) == 0 {
		return body, nil
	}
	saved, err := j.readBody(ctx, string(raw))
	if err != nil {
		return "", err
	}
	fields[key] = json.RawMessage(saved)
	out, err := json.Marshal(fields)
	return string(out), err
}

func (j *Journal) retainRequestBody(ctx context.Context, operation, body string) (string, error) {
	if operation == protocol.OpManagerNativeReply {
		return j.retainNativeBody(ctx, body, false)
	}
	return j.retainBody(ctx, body)
}
func (j *Journal) readRequestBody(ctx context.Context, operation, body string) (string, error) {
	raw, err := j.readBody(ctx, body)
	if err != nil {
		return "", err
	}
	if operation == protocol.OpManagerNativeReply {
		return j.readNativeBody(ctx, raw, false)
	}
	return raw, nil
}
