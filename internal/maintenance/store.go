package maintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	_ "modernc.org/sqlite"
)

type Native interface {
	NewThread(context.Context, protocol.MaintenanceBackend, string, string) (NativeThread, error)
	Read(context.Context, protocol.MaintenanceBackend, string, NativeReadMode) (NativeState, error)
	Start(context.Context, protocol.MaintenanceBackend, NativeThread, string, string) (string, error)
	Interrupt(context.Context, protocol.MaintenanceBackend, string, string) error
}

// NativeThread retains the resolved permissions across controller restarts.
type NativeThread struct {
	ID, Path    string
	Sandbox     map[string]any
	RequireIdle bool
}

type NativeReadMode int

const (
	ReadMetadata NativeReadMode = iota
	ReadWhenIdle
	ReadFullHistory
)

func nativeThread(s *protocol.MaintenanceSession) NativeThread {
	return NativeThread{ID: s.SessionID, Path: s.RolloutPath, Sandbox: s.SandboxPolicy, RequireIdle: s.ScheduledAuditID != ""}
}

// ErrNotSent proves the native mutation was never submitted or was rejected.
var ErrNotSent = errors.New("native operation was not accepted")

// ErrThreadUnavailable is an explicit, identity-matching provider rejection.
// It can retire an idle thread but cannot resolve an outstanding native operation.
var ErrThreadUnavailable = errors.New("native thread is unavailable")

type NativeState struct {
	Busy     bool
	Unusable bool
	Turns    []NativeTurn
}
type NativeTurn struct{ ID, InputID, Status string }

type Options struct {
	Head           func(context.Context, string) (string, error)
	Log            *slog.Logger
	RuntimeID      string
	ArtifactSHA256 string
	Now            func() time.Time
	ID             func() string
	Native         Native
}

type Controller struct {
	mu    sync.Mutex
	cfg   Config
	opts  Options
	db    *sql.DB
	lock  *os.File
	state protocol.MaintenanceStatus
	fault error
}

func Open(ctx context.Context, cfg Config, opts Options) (*Controller, error) {
	if opts.Now == nil || opts.ID == nil || opts.Native == nil {
		return nil, errors.New("maintenance: require clock, IDs and native adapter")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Head == nil {
		opts.Head = func(ctx context.Context, dir string) (string, error) {
			cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
			raw, err := cmd.Output()
			return strings.TrimSpace(string(raw)), err
		}
	}
	if cfg.CheckSeconds == 0 {
		cfg.CheckSeconds = 300
	}
	if err := ValidateBackend(cfg.Backend); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("maintenance: state directory must be private")
	}
	lock, err := lockState(filepath.Join(cfg.StateDir, "controller.lock"))
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.StateDir, "maintenance.db"))
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c := &Controller{cfg: cfg, opts: opts, db: db, lock: lock}
	fail := func(e error) (*Controller, error) { _ = c.Close(); return nil, e }
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS state (id INTEGER PRIMARY KEY CHECK(id=1), body BLOB NOT NULL); CREATE TABLE IF NOT EXISTS receipts (id TEXT PRIMARY KEY, hash TEXT NOT NULL, revision INTEGER NOT NULL, body BLOB NOT NULL); CREATE TABLE IF NOT EXISTS history (seq INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, body BLOB NOT NULL);`); err != nil {
		return fail(err)
	}
	var raw []byte
	err = db.QueryRowContext(ctx, `SELECT body FROM state WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		c.state = protocol.MaintenanceStatus{Format: 1, Backend: cfg.Backend, Cases: []protocol.MaintenanceCase{}, History: []protocol.MaintenanceSession{}, Pending: []string{}}
		if err := c.save(ctx); err != nil {
			return fail(err)
		}
	} else if err != nil {
		return fail(err)
	} else if err := json.Unmarshal(raw, &c.state); err != nil {
		return fail(err)
	}
	if c.state.Format != 1 {
		return fail(errors.New("maintenance: unsupported durable format"))
	}
	imported, err := c.importLegacyAudit(ctx)
	if err != nil {
		return fail(err)
	}
	if len(c.state.History) > 0 || imported {
		if err := c.save(ctx); err != nil {
			return fail(err)
		}
	}
	return c, nil
}

// Old creating snapshots stored the pending trigger and the receipts but did
// not associate them. Import only an unambiguous first audit, or retain the
// cutoff so an operator can reconcile the original receipt from its evidence.
func (c *Controller) importLegacyAudit(ctx context.Context) (bool, error) {
	s := &c.state
	a := s.Active
	if a == nil || a.Phase != "creating" || a.SessionProfile != "" ||
		s.LegacyAuditThrough != 0 || len(s.PendingAudits) != 0 || !slices.Contains(s.Pending, "audit") ||
		s.ScheduledHour == "" || s.ScheduledHour != time.Unix(s.LastAudit, 0).UTC().Format("2006-01-02T15") {
		return false, nil
	}
	var count int
	var onlyID sql.NullString
	err := c.db.QueryRowContext(ctx, `SELECT count(*), min(id) FROM receipts
		WHERE revision<=? AND json_extract(body,'$.Action')='audit'`, s.Revision).Scan(&count, &onlyID)
	if err != nil {
		return false, err
	}
	s.LegacyAuditThrough = s.Revision
	if count == 1 && s.LastAuditCompleted == 0 {
		original, err := c.legacyAuditReceipt(ctx, onlyID.String)
		if err != nil {
			return false, err
		}
		c.bindLegacyAudit(original)
	}
	return true, nil
}

func (c *Controller) legacyAuditReceipt(ctx context.Context, id string) (protocol.MaintenanceRequest, error) {
	var raw []byte
	err := c.db.QueryRowContext(ctx, `SELECT body FROM receipts WHERE id=? AND revision<=?`, id, c.state.LegacyAuditThrough).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.MaintenanceRequest{}, protocol.Errorf(protocol.CodeInvalidRequest, "original audit receipt must predate the legacy reconciliation")
	}
	if err != nil {
		return protocol.MaintenanceRequest{}, err
	}
	var original struct {
		Action  string
		Request protocol.MaintenanceRequest
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		return protocol.MaintenanceRequest{}, err
	}
	if original.Action != "audit" || original.Request.RequestID != id {
		return protocol.MaintenanceRequest{}, protocol.Errorf(protocol.CodeInvalidRequest, "receipt does not identify an original audit request")
	}
	return original.Request, nil
}

func (c *Controller) bindLegacyAudit(original protocol.MaintenanceRequest) {
	if original.ScheduledAudit {
		c.state.PendingAudits = append([]string{original.RequestID}, c.state.PendingAudits...)
	}
	c.state.LegacyAuditThrough = 0
}

func (c *Controller) Close() error {
	if closer, ok := c.opts.Native.(io.Closer); ok {
		_ = closer.Close()
	}
	err := c.db.Close()
	e := c.lock.Close()
	if err != nil {
		return err
	}
	return e
}
func (c *Controller) save(ctx context.Context) error {
	if c.fault != nil {
		return c.fault
	}
	c.state.Revision++
	raw, err := marshalState(c.state)
	if err == nil {
		var tx *sql.Tx
		tx, err = c.db.BeginTx(ctx, nil)
		if err == nil {
			defer tx.Rollback()
			err = c.writeHistory(ctx, tx)
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO state(id,body) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body`, raw)
			}
			if err == nil {
				err = tx.Commit()
			}
		}
	}
	if err != nil {
		c.fault = err
	} else {
		c.state.History = nil
	}
	return err
}

func (c *Controller) History(ctx context.Context, after int64, limit int) ([]protocol.MaintenanceSession, int64, bool, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, 0, false, protocol.Errorf(protocol.CodeInvalidRequest, "invalid history pagination")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := c.db.QueryContext(ctx, `SELECT seq,body FROM history WHERE seq>? ORDER BY seq LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	items := []protocol.MaintenanceSession{}
	next := after
	more := false
	for rows.Next() {
		var seq int64
		var raw []byte
		if err := rows.Scan(&seq, &raw); err != nil {
			return nil, 0, false, err
		}
		if len(items) == limit {
			more = true
			break
		}
		var session protocol.MaintenanceSession
		if err := json.Unmarshal(raw, &session); err != nil {
			return nil, 0, false, err
		}
		items = append(items, session)
		next = seq
	}
	return items, next, more, rows.Err()
}
func (c *Controller) Status() protocol.MaintenanceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, _ := json.Marshal(c.state)
	var s protocol.MaintenanceStatus
	_ = json.Unmarshal(raw, &s)
	if c.fault != nil {
		s.NativeError = "maintenance: persistence failure; controller restart required"
	} else if s.Active != nil && s.Active.Phase == "creating" && s.Active.CreationError != "" {
		s.NativeError = errCreationNeedsReconciliation.Error() + ": " + s.Active.CreationError
	} else if s.Failure != nil {
		s.NativeError = "maintenance: native turn " + s.Failure.Outcome + "; inspect session history and request audit, resume or backend adoption before retrying"
	} else if s.LegacyAuditThrough != 0 && s.NativeError == "" {
		s.NativeError = "maintenance: original legacy audit receipt requires reconciliation"
	}
	s.OpenCases = 0
	for _, v := range s.Cases {
		if v.Status == "open" {
			s.OpenCases++
		}
	}
	return s
}
func digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func marshalState(v protocol.MaintenanceStatus) ([]byte, error) {
	v.History = nil
	return json.Marshal(v)
}

func (c *Controller) writeHistory(ctx context.Context, tx *sql.Tx) error {
	for _, session := range c.state.History {
		body, err := json.Marshal(session)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history(id,body) VALUES(?,?)`, session.ID, body); err != nil {
			return err
		}
	}
	return nil
}
