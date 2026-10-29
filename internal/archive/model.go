// Package archive captures and reads offline ledgers without opening a business store.
package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const FormatVersion = 1
const MinimumSchema = 53
const MaximumSchema = 57

// CaptureOptions describes owner-selected stopped material, never a live service.
type CaptureOptions struct {
	Database       string
	BlobRoot       string
	Destination    string
	WritersStopped bool
	CapturedAt     time.Time
	SourceBuild    string
	Resources      []ResourceSource
}

type ResourceSource struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	HostID         string `json:"host_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	ParentThreadID string `json:"parent_thread_id,omitempty"`
	Scope          string `json:"scope"`
	Path           string `json:"path,omitempty"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
}

type Payload struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Resource struct {
	JournalSchema  int      `json:"journal_schema,omitempty"`
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	HostID         string   `json:"host_id,omitempty"`
	RunID          string   `json:"run_id,omitempty"`
	ThreadID       string   `json:"thread_id,omitempty"`
	ParentThreadID string   `json:"parent_thread_id,omitempty"`
	Scope          string   `json:"scope"`
	Status         string   `json:"status"`
	Reason         string   `json:"reason,omitempty"`
	Files          []string `json:"files,omitempty"`
}

type Original struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type,omitempty"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

type Migration struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
}

type Manifest struct {
	FormatVersion int         `json:"format_version"`
	CreatedAt     time.Time   `json:"created_at"`
	DeploymentID  string      `json:"deployment_id"`
	Schema        int         `json:"schema"`
	Migrations    []Migration `json:"migrations"`
	SourceBuild   string      `json:"source_build,omitempty"`
	Snapshot      string      `json:"snapshot"`
	Files         []Payload   `json:"files"`
	Originals     []Original  `json:"originals"`
	Resources     []Resource  `json:"resources"`
}

// Reader owns only a sealed archive connection. It has no business write path.
type Reader struct {
	root     string
	manifest Manifest
	db       *sql.DB
}

func (r *Reader) Close() error { return r.db.Close() }
func (r *Reader) Manifest() Manifest {
	// Return detached metadata so callers cannot change the reader's file map.
	b, _ := json.Marshal(r.manifest)
	var copy Manifest
	_ = json.Unmarshal(b, &copy)
	return copy
}

var ErrUnsupportedSchema = errors.New("archive: unsupported ledger schema")

// Records returns one fixed historical table, without interpreting old rows as new issues.
func (r *Reader) Records(ctx context.Context, kind, after string, limit int) ([]map[string]any, error) {
	table, ok := objectTables[kind]
	if !ok || (table.minimum > r.manifest.Schema) {
		return nil, fmt.Errorf("archive: unsupported object kind %q for schema %d", kind, r.manifest.Schema)
	}
	if limit < 1 || limit > 200 {
		return nil, errors.New("archive: limit must be between 1 and 200")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM `+table.name+` WHERE `+table.id+`>? ORDER BY `+table.id+` LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("archive: list %s: %w", kind, err)
	}
	defer rows.Close()
	return scanRows(rows)
}

func (r *Reader) Record(ctx context.Context, kind, id string) (map[string]any, error) {
	table, ok := objectTables[kind]
	if !ok || table.minimum > r.manifest.Schema {
		return nil, fmt.Errorf("archive: unsupported object kind %q", kind)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM `+table.name+` WHERE `+table.id+`=?`, id)
	if err != nil {
		return nil, fmt.Errorf("archive: read %s: %w", kind, err)
	}
	defer rows.Close()
	records, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, sql.ErrNoRows
	}
	return records[0], nil
}

type objectTable struct {
	name, id string
	minimum  int
}

var objectTables = map[string]objectTable{
	"work_item":          {"work_items", "work_item_id", 53},
	"issue_control":      {"issue_controls", "issue_id", 54},
	"task":               {"tasks", "task_id", 53},
	"run":                {"runs", "run_id", 53},
	"artifact":           {"artifacts", "artifact_id", 53},
	"acceptance":         {"acceptances", "acceptance_id", 53},
	"decision":           {"decisions", "decision_id", 53},
	"message":            {"conversation_messages", "message_id", 53},
	"agent_conversation": {"agent_conversations", "conversation_id", 53},
	"run_execution":      {"run_agent_executions", "run_id", 57},
}

func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		addresses := make([]any, len(columns))
		for i := range values {
			addresses[i] = &values[i]
		}
		if err := rows.Scan(addresses...); err != nil {
			return nil, err
		}
		record := map[string]any{}
		for i, column := range columns {
			record[column] = values[i]
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// OpenOriginal maps an opaque historical ID to a listed immutable payload.
func (r *Reader) OpenOriginal(kind, id string) (Original, io.ReadCloser, error) {
	for _, original := range r.manifest.Originals {
		if original.Kind == kind && original.ID == id {
			f, err := openPayload(r.root, original.Path)
			return original, f, err
		}
	}
	return Original{}, nil, sql.ErrNoRows
}

// OpenResource accepts an indexed payload, never a caller-supplied local path.
func (r *Reader) OpenResource(id string, index int) (io.ReadCloser, error) {
	for _, resource := range r.manifest.Resources {
		if resource.ID == id {
			if index < 0 || index >= len(resource.Files) {
				return nil, sql.ErrNoRows
			}
			return openPayload(r.root, resource.Files[index])
		}
	}
	return nil, sql.ErrNoRows
}
