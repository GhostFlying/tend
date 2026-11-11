package rollout

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
)

// references recognizes structured native links, never paths in prose or commands.
func references(value any, transcript string) []string {
	root := strings.TrimSuffix(transcript, ".jsonl") + ".artifacts"
	seen := map[string]bool{}
	out := []string{}
	var visit func(any, int)
	visit = func(value any, depth int) {
		if depth > 32 || len(out) >= 64 {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			if v["role"] == "user" || v["role"] == "developer" {
				return
			}
			for _, key := range []string{"artifact_path", "blob_path", "output_file"} {
				path, ok := v[key].(string)
				if !ok {
					continue
				}
				if key == "output_file" {
					if _, ok := v["chunk_id"]; !ok {
						continue
					}
				}
				if !filepath.IsAbs(path) {
					path = filepath.Join(root, path)
				}
				relative, err := filepath.Rel(root, path)
				if err == nil && filepath.IsLocal(relative) && !seen[relative] {
					seen[relative] = true
					out = append(out, relative)
				}
			}
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				switch key {
				case "arguments", "input", "command", "prompt", "stdin":
					continue
				}
				visit(v[key], depth+1)
			}
		case []any:
			for _, item := range v {
				visit(item, depth+1)
			}
		case string:
			if len(v) > maxNativeLine || !strings.Contains(v, "output_file") {
				return
			}
			var nested any
			if json.Unmarshal([]byte(v), &nested) == nil {
				visit(nested, depth+1)
			}
		}
	}
	visit(value, 0)
	return out
}
func (r *Reader) openReference(transcript, relative string) (*os.File, error) {
	root := strings.TrimSuffix(transcript, ".jsonl") + ".artifacts"
	var dir *os.Root
	for _, native := range r.roots {
		rel, err := relativeToRoot(native, root)
		if err != nil {
			continue
		}
		base, err := os.OpenRoot(native)
		if err != nil {
			continue
		}
		info, statErr := base.Lstat(rel)
		if statErr == nil && info.Mode()&os.ModeSymlink == 0 {
			dir, err = base.OpenRoot(rel)
		}
		_ = base.Close()
		if dir != nil && err == nil {
			break
		}
	}
	if dir == nil {
		return nil, os.ErrPermission
	}
	defer dir.Close()
	return dir.Open(relative)
}
func (r *Reader) referenceViews(path string, refs []string) []protocol.RolloutPreviewReference {
	out := make([]protocol.RolloutPreviewReference, 0, len(refs))
	for i, relative := range refs {
		f, err := r.openReference(path, relative)
		if f != nil {
			_ = f.Close()
		}
		out = append(out, protocol.RolloutPreviewReference{ID: int64(i + 1), Name: filepath.Base(relative), Missing: err != nil})
	}
	return out
}
func chunk(data []byte, offset int64) (*protocol.RolloutPreviewContent, error) {
	if offset < 0 || offset > int64(len(data)) || (offset < int64(len(data)) && !utf8.RuneStart(data[offset])) {
		return nil, errors.New("invalid content cursor")
	}
	end := min(int(offset)+contentBytes, len(data))
	for end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	if !utf8.Valid(data[offset:end]) {
		return nil, errors.New("content is not UTF-8")
	}
	return &protocol.RolloutPreviewContent{Text: string(data[offset:end]), NextOffset: int64(end), More: end < len(data)}, nil
}
func (r *Reader) content(path string, rec record, q protocol.RolloutPreviewQuery, generation string) protocol.RolloutPreview {
	if q.Field == "input" && q.Reference == 0 && rec.input != nil {
		rec = *rec.input
	}
	row, p, raw, err := r.nativeRecord(path, rec)
	if err != nil {
		return state("unavailable")
	}
	refs := references(p, path)
	result := protocol.RolloutPreview{State: "ready", Generation: generation}
	if q.Reference > 0 {
		if q.Reference > int64(len(refs)) {
			return state("cursor_invalid")
		}
		f, err := r.openReference(path, refs[q.Reference-1])
		if err != nil {
			return state("missing_reference")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return state("missing_reference")
		}
		if q.Offset < 0 || q.Offset > info.Size() {
			return state("cursor_invalid")
		}
		if _, err = f.Seek(q.Offset, io.SeekStart); err != nil {
			return state("unavailable")
		}
		data, err := io.ReadAll(io.LimitReader(f, contentBytes+4))
		if err != nil {
			return state("unavailable")
		}
		part, err := chunk(data, 0)
		if err != nil {
			return state("format_unsupported")
		}
		part.NextOffset += q.Offset
		part.More = part.NextOffset < info.Size()
		result.Content = part
		return result
	}
	if q.Field != "" && q.Field != "raw" {
		data, err := r.readRecord(path, rec, false)
		if err != nil {
			return state("unavailable")
		}
		var value any
		switch q.Field {
		case "input":
			for _, key := range []string{"command", "arguments", "query", "path", "prompt", "changes", "action"} {
				if v, ok := data[key]; ok {
					value = v
					break
				}
			}
		case "output":
			for _, key := range []string{"aggregatedOutput", "result", "contentItems", "error"} {
				if v, ok := data[key]; ok && v != nil {
					value = v
					break
				}
			}
		case "text":
			value = data["text"]
		default:
			return state("cursor_invalid")
		}
		if text, ok := value.(string); ok {
			raw = []byte(text)
		} else if value == nil {
			raw = nil
		} else {
			raw, _ = json.MarshalIndent(value, "", "  ")
		}
	} else if rec.part >= 0 {
		// A history mutation can hold several messages; a diagnostic record reads only its own item.
		raw, _ = json.MarshalIndent(map[string]any{"type": row.Type, "timestamp": row.Timestamp, "payload": p}, "", "  ")
	}
	if q.Field == "" || q.Field == "raw" {
		var formatted bytes.Buffer
		if json.Indent(&formatted, raw, "", "  ") == nil {
			raw = formatted.Bytes()
		}
	}
	result.Content, err = chunk(raw, q.Offset)
	if err != nil {
		return state("cursor_invalid")
	}
	result.Content.References = r.referenceViews(path, refs)
	return result
}
