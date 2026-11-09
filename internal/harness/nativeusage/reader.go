// Package nativeusage reads durable request measurements without starting a CLI.
// It uses only the standard library and the wire types shared with the server.
package nativeusage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Source is an explicitly registered native history location. Paths remain on
// the executor; no owner-supplied path is accepted by the server upload API.
type Source struct {
	NoDispatchProven bool     `json:"no_dispatch_proven,omitempty"`
	WriterID         string   `json:"writer_id,omitempty"`
	ActorName        string   `json:"actor_name,omitempty"`
	ActorID          string   `json:"actor_id,omitempty"`
	ActorCategories  []string `json:"actor_categories,omitempty"`
	Path             string   `json:"path"`
	Provider         string   `json:"provider"`
	ThreadID         string   `json:"thread_id"`
	SessionID        string   `json:"session_id"`
	Purpose          string   `json:"purpose,omitempty"`
	ParentSessionID  string   `json:"parent_session_id,omitempty"`
}

type Cursor struct {
	LastModel string            `json:"last_model,omitempty"`
	FileID    string            `json:"file_id,omitempty"`
	Anchor    string            `json:"anchor,omitempty"`
	Offset    int64             `json:"offset"`
	Prefix    string            `json:"prefix"`
	Models    map[string]string `json:"models"`
}

type Page struct {
	Samples    []protocol.TokenUsageSample
	Cursor     Cursor
	SessionID  string
	More       bool
	Incomplete bool
}

type metadata struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	Version    string          `json:"cli_version"`
	ForkedFrom string          `json:"forked_from_id"`
	Source     json.RawMessage `json:"source"`
}

type row struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Ordinal   *int64          `json:"ordinal"`
	Payload   json.RawMessage `json:"payload"`
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func header(f *os.File) (metadata, string, error) {
	r := bufio.NewReader(f)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return metadata{}, "", fmt.Errorf("native usage: incomplete metadata: %w", err)
	}
	var row row
	var meta metadata
	if json.Unmarshal(line, &row) != nil || row.Type != "session_meta" || json.Unmarshal(row.Payload, &meta) != nil || meta.ID == "" {
		return meta, "", errors.New("native usage: unsupported history metadata")
	}
	if meta.SessionID == "" {
		meta.SessionID = meta.ID
	}
	return meta, digest(line), nil
}

// Read returns complete records only. The caller must durably enqueue Page and
// its cursor together before advancing its saved reading position.
func Read(ctx context.Context, src Source, cursor Cursor, limit int) (Page, error) {
	return read(ctx, src, cursor, limit, nil, true)
}
func read(ctx context.Context, src Source, cursor Cursor, limit int, index *Discovery, final bool) (p Page, err error) {
	p = Page{Samples: []protocol.TokenUsageSample{}, Cursor: cursor}
	if src.Provider != "codex" && src.Provider != "traex" {
		return p, errors.New("native usage: unsupported provider")
	}
	if index != nil {
		index.opens.Add(1)
	}
	f, err := os.Open(src.Path)
	if err != nil {
		return p, fmt.Errorf("native usage: open history: %w", err)
	}
	defer f.Close()
	var meta metadata
	var prefix string
	if index == nil {
		meta, prefix, err = header(f)
	} else {
		meta, prefix, err = index.fileHeader(src.Path, f, final)
	}
	if err != nil {
		return p, err
	}
	if src.ThreadID != "" && meta.ID != src.ThreadID && (src.SessionID == "" || src.SessionID != meta.SessionID) {
		return p, ErrIdentity
	}
	if src.SessionID != "" && src.SessionID != meta.SessionID {
		return p, ErrIdentity
	}
	p.SessionID = meta.SessionID
	st, err := f.Stat()
	if err != nil {
		return p, fmt.Errorf("native usage: stat history: %w", err)
	}
	anchorChanged := cursor.Anchor != "" && cursor.Anchor != cursorAnchor(f, cursor.Offset)
	if anchorChanged && index != nil {
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return p, err
		}
		meta, prefix, err = index.fileHeader(src.Path, f, true)
		if err != nil {
			return p, err
		}
		if (src.ThreadID != "" && meta.ID != src.ThreadID && (src.SessionID == "" || src.SessionID != meta.SessionID)) || (src.SessionID != "" && src.SessionID != meta.SessionID) {
			return p, ErrIdentity
		}
		p.SessionID = meta.SessionID
	}
	if cursor.Prefix != prefix || cursor.Offset > st.Size() || cursor.Offset < 0 || (cursor.FileID != "" && cursor.FileID != fileIdentity(f, st)) || anchorChanged {
		p.Cursor = Cursor{Prefix: prefix}
	}
	p.Cursor.FileID = fileIdentity(f, st)
	defer func() { p.Cursor.Anchor = cursorAnchor(f, p.Cursor.Offset) }()
	p.Cursor.Models = cloneModels(p.Cursor.Models)
	if _, err = f.Seek(p.Cursor.Offset, io.SeekStart); err != nil {
		return p, fmt.Errorf("native usage: seek: %w", err)
	}
	if limit <= 0 {
		limit = 256
	}
	r := bufio.NewReader(f)
	for len(p.Samples) < limit {
		if err = ctx.Err(); err != nil {
			return p, err
		}
		line, readErr := r.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			p.Incomplete = len(line) > 0
			return p, nil
		}
		if readErr != nil {
			return p, fmt.Errorf("native usage: read: %w", readErr)
		}
		var rec row
		if err = json.Unmarshal(line, &rec); err != nil {
			return p, errors.New("native usage: malformed complete record")
		}
		if rec.Type == "turn_context" {
			var v struct {
				Turn  string `json:"turn_id"`
				Model string `json:"model"`
			}
			if err = json.Unmarshal(rec.Payload, &v); err != nil {
				return p, errors.New("native usage: malformed turn context")
			}
			if v.Turn != "" && v.Model != "" {
				p.Cursor.Models[v.Turn] = v.Model
				p.Cursor.LastModel = v.Model
			}
		}
		if rec.Type == "token_usage_record" {
			sample, e := sample(rec, meta, src, p.Cursor.Models, p.Cursor.LastModel)
			if e != nil {
				return p, e
			}
			p.Samples = append(p.Samples, sample)
		}
		p.Cursor.Offset += int64(len(line))
	}
	p.More = true
	return p, nil
}

func cloneModels(models map[string]string) map[string]string {
	result := map[string]string{}
	for k, v := range models {
		result[k] = v
	}
	return result
}

func sample(r row, meta metadata, src Source, models map[string]string, lastModel string) (protocol.TokenUsageSample, error) {
	var v struct {
		Thread   string `json:"thread_id"`
		Turn     string `json:"turn_id"`
		RootTurn string `json:"root_turn_id"`
		Session  string `json:"session_id"`
		Response string `json:"response_id"`
		Usage    struct {
			Input      *int64 `json:"input_tokens"`
			Read       *int64 `json:"cached_input_tokens"`
			Write      *int64 `json:"cache_creation_input_tokens"`
			WriteCodex *int64 `json:"cache_write_input_tokens"`
			Output     *int64 `json:"output_tokens"`
			Total      *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(r.Payload, &v) != nil || v.Thread == "" || v.Turn == "" || v.Response == "" {
		return protocol.TokenUsageSample{}, errors.New("native usage: missing request identity")
	}
	if v.Session == "" {
		v.Session = meta.SessionID
	}
	if v.Session != meta.SessionID {
		return protocol.TokenUsageSample{}, ErrIdentity
	}
	key := strings.Join([]string{src.Provider, v.Thread, v.Turn, v.Response, r.Timestamp}, "\x00")
	if r.Ordinal != nil {
		if *r.Ordinal < 0 {
			return protocol.TokenUsageSample{}, errors.New("native usage: negative ordinal")
		}
		key = strings.Join([]string{src.Provider, v.Thread, strconv.FormatInt(*r.Ordinal, 10)}, "\x00")
	}
	if (src.Provider == "codex" && v.Usage.Write != nil && v.Usage.WriteCodex == nil) || (src.Provider == "traex" && v.Usage.WriteCodex != nil && v.Usage.Write == nil) {
		return protocol.TokenUsageSample{}, errors.New("native usage: provider does not match the verified counter dialect")
	}
	if v.Usage.Write == nil {
		v.Usage.Write = v.Usage.WriteCodex
	}
	encoded, _ := json.Marshal(r)
	var original any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&original); err != nil {
		return protocol.TokenUsageSample{}, err
	}
	canonical, _ := json.Marshal(original)
	s := protocol.TokenUsageSample{RecordID: digest([]byte(key)), NativeContentHash: digest(canonical), RootTurnID: v.RootTurn, Provider: src.Provider, NativeVersion: meta.Version, SessionID: v.Session, Ordinal: r.Ordinal, Source: "native-ledger/" + src.Provider + "/" + v.Thread, RequestID: v.Response, ThreadID: v.Thread, TurnID: v.Turn, Revision: 1, Settlement: "final", Profile: src.Provider + "-native-ledger", Model: models[v.Turn], CounterSource: "native-normalized", Purpose: src.Purpose, Input: v.Usage.Input, CacheRead: v.Usage.Read, CacheWrite: v.Usage.Write, Output: v.Usage.Output, Total: v.Usage.Total}
	s.ParentSessionID = meta.ForkedFrom
	if src.ParentSessionID != "" {
		s.ParentSessionID = src.ParentSessionID
	}
	var parent struct {
		Subagent struct {
			Spawn struct {
				Parent string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(meta.Source, &parent) == nil {
		s.ParentThreadID = parent.Subagent.Spawn.Parent
	}
	if s.Model != "" {
		s.ModelSource = "native-turn-context"
	} else if lastModel != "" {
		s.Model = lastModel
		s.ModelSource = "native-preceding-context"
	}
	if stamp, err := time.Parse(time.RFC3339Nano, r.Timestamp); err == nil && stamp.UnixMilli() >= 0 {
		n := stamp.UnixMilli()
		s.OccurredAt = &n
		s.TimeSource = "native-recorded-completion"
	}
	for _, n := range []*int64{s.Input, s.CacheRead, s.CacheWrite, s.Output, s.Total} {
		if n != nil && (*n < 0 || *n > 1<<50) {
			return s, errors.New("native usage: invalid counter")
		}
	}
	return s, nil
}

// Discover inspects metadata in the registered history store and returns only
// continuations/children of the already verified Session. It never imports an
// unrelated session merely because it shares a provider home or repository.
func Discover(ctx context.Context, src Source) ([]Source, error) {
	return NewDiscovery().Cycle(0).Discover(ctx, src, false)
}

func (c *DiscoveryCycle) Discover(ctx context.Context, src Source, force bool) ([]Source, error) {
	var meta metadata
	anchorExists := false
	c.index.opens.Add(1)
	f, err := os.Open(src.Path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) || src.ThreadID == "" || src.SessionID == "" {
			return nil, fmt.Errorf("native usage: locate source: %w", err)
		}
		meta = metadata{ID: src.ThreadID, SessionID: src.SessionID}
	} else {
		anchorExists = true
		meta, _, err = c.index.fileHeader(src.Path, f, force)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	if src.ThreadID != "" && src.ThreadID != meta.ID && src.SessionID != meta.SessionID {
		return nil, ErrIdentity
	}
	src.SessionID = meta.SessionID
	root := filepath.Dir(src.Path)
	for dir := root; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "sessions" || filepath.Base(dir) == "archived_sessions" {
			root = dir
			break
		}
	}
	roots := []string{root}
	if filepath.Base(root) == "sessions" || filepath.Base(root) == "archived_sessions" {
		other := "archived_sessions"
		if filepath.Base(root) == "archived_sessions" {
			other = "sessions"
		}
		sibling := filepath.Join(filepath.Dir(root), other)
		if _, e := os.Stat(sibling); e == nil {
			roots = append(roots, sibling)
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, fmt.Errorf("native usage: inspect registered store: %w", e)
		}
	}

	storeRoot := root
	if filepath.Base(root) == "sessions" || filepath.Base(root) == "archived_sessions" {
		storeRoot = filepath.Dir(root)
	}
	entries, err := c.entries(ctx, src.Provider+"/"+storeRoot, roots, force)
	if err != nil {
		return nil, err
	}
	if anchorExists {
		found := false
		for _, entry := range entries {
			found = found || entry.path == src.Path
		}
		if !found {
			entries = append(append([]historyHeader{}, entries...), historyHeader{path: src.Path, meta: meta})
		}
	}
	var sources []Source
	parents := map[string]string{}
	for _, entry := range entries {
		m := entry.meta
		if m.SessionID == meta.SessionID {
			child := src
			child.Path, child.ThreadID = entry.path, m.ID
			sources = append(sources, child)
			parents[m.ID] = metadataParent(m)
		}
	}
	// A registered child does not establish ownership of its ancestors or
	// siblings, even when the CLI groups them under one Session.
	if src.ThreadID != meta.SessionID {
		allowed := map[string]bool{src.ThreadID: true}
		for changed := true; changed; {
			changed = false
			for child, parent := range parents {
				if !allowed[child] && allowed[parent] {
					allowed[child] = true
					changed = true
				}
			}
		}
		owned := sources[:0]
		for _, source := range sources {
			if allowed[source.ThreadID] {
				owned = append(owned, source)
			}
		}
		sources = owned
	}
	if len(sources) == 0 {
		return nil, ErrHistoryUnavailable
	}
	return sources, nil
}

// cursorAnchor detects an in-place rewrite around the durable boundary. File
// identity additionally detects replacement even when metadata was copied.
func cursorAnchor(f *os.File, offset int64) string {
	if offset <= 0 {
		return ""
	}
	n := min(offset, 4096)
	b := make([]byte, n)
	if _, err := f.ReadAt(b, offset-n); err != nil {
		return ""
	}
	return digest(b)
}

func metadataParent(meta metadata) string {
	var parent struct {
		Subagent struct {
			Spawn struct {
				Parent string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(meta.Source, &parent) != nil {
		return ""
	}
	return parent.Subagent.Spawn.Parent
}
