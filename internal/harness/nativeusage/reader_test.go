package nativeusage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func line(kind string, ordinal int, payload any) string {
	b, _ := json.Marshal(map[string]any{"type": kind, "ordinal": ordinal, "timestamp": "2026-10-30T12:00:00.123Z", "payload": payload})
	return string(b) + "\n"
}
func usage(turn, response string, total int64) any {
	return map[string]any{"thread_id": "thread", "session_id": "session", "turn_id": turn, "response_id": response, "usage": map[string]any{"input_tokens": total - 10, "cached_input_tokens": 50, "cache_creation_input_tokens": 0, "output_tokens": 10, "reasoning_output_tokens": 2, "total_tokens": total}, "thread_token_usage": map[string]any{"total_tokens": 999999999}}
}
func fixture(t *testing.T) (Source, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sessions", "2026", "history.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	header := line("session_meta", 0, map[string]any{"id": "thread", "session_id": "session", "cli_version": "test"})
	return Source{Path: p, Provider: "traex", ThreadID: "thread"}, header
}
func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalResumeRepeatedResponseAndModelSwitch(t *testing.T) {
	s, h := fixture(t)
	a := line("turn_context", 1, map[string]string{"turn_id": "first", "model": "model-a"}) + line("token_usage_record", 2, usage("first", "resp_1", 110))
	b := line("turn_context", 3, map[string]string{"turn_id": "second", "model": "model-b"}) + line("token_usage_record", 4, usage("second", "resp_1", 220))
	write(t, s.Path, h+a+b)
	p, err := Read(t.Context(), s, Cursor{}, 1)
	if err != nil || len(p.Samples) != 1 || p.Samples[0].Model != "model-a" {
		t.Fatal(p, err)
	}
	q, err := Read(t.Context(), s, p.Cursor, 10)
	if err != nil || len(q.Samples) != 1 || q.Samples[0].Model != "model-b" {
		t.Fatal(q, err)
	}
	if p.Samples[0].RecordID == q.Samples[0].RecordID || p.Samples[0].RequestID != q.Samples[0].RequestID {
		t.Fatal("response ID was mistaken for ledger identity")
	}
	if *p.Samples[0].Total+*q.Samples[0].Total != 330 || p.Samples[0].CacheWrite == nil || *p.Samples[0].CacheWrite != 0 {
		t.Fatal("cumulative counters or field presence corrupted")
	}
	if q.Samples[0].SessionID != "session" || q.Samples[0].OccurredAt == nil || q.Samples[0].TimeSource != "native-recorded-completion" {
		t.Fatal(q)
	}
	r, err := Read(t.Context(), s, q.Cursor, 10)
	if err != nil || len(r.Samples) != 0 {
		t.Fatal(r, err)
	}
}

func TestPartialLineAndTruncation(t *testing.T) {
	s, h := fixture(t)
	u := line("token_usage_record", 2, usage("turn", "resp", 110))
	write(t, s.Path, h+u[:len(u)/2])
	p, err := Read(t.Context(), s, Cursor{}, 10)
	if err != nil || len(p.Samples) != 0 || p.Cursor.Offset != int64(len(h)) {
		t.Fatal(p, err)
	}
	write(t, s.Path, h+u)
	q, err := Read(t.Context(), s, p.Cursor, 10)
	if err != nil || len(q.Samples) != 1 {
		t.Fatal(q, err)
	}
	write(t, s.Path, h)
	r, err := Read(t.Context(), s, q.Cursor, 10)
	if err != nil || len(r.Samples) != 0 || r.Cursor.Offset != int64(len(h)) {
		t.Fatal(r, err)
	}
	write(t, s.Path, h+u)
	r, err = Read(t.Context(), s, r.Cursor, 10)
	if err != nil || len(r.Samples) != 1 || r.Samples[0].RecordID != q.Samples[0].RecordID {
		t.Fatal(r, err)
	}
}

func TestDiscoverOnlyRegisteredSessionAndPreserveContinuationIdentity(t *testing.T) {
	s, h := fixture(t)
	u := line("token_usage_record", 2, usage("turn", "resp", 110))
	write(t, s.Path, h+u)
	copyPath := filepath.Join(filepath.Dir(s.Path), "continuation.jsonl")
	write(t, copyPath, h+u)
	// Keep the row type intact while changing only the metadata identity.
	other := line("session_meta", 0, map[string]string{"id": "other", "session_id": "other"})
	write(t, filepath.Join(filepath.Dir(s.Path), "other.jsonl"), other)
	sources, err := Discover(t.Context(), s)
	if err != nil || len(sources) != 2 {
		t.Fatal(sources, err)
	}
	p, err := Read(t.Context(), sources[0], Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Read(t.Context(), sources[1], Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if p.Samples[0].RecordID != q.Samples[0].RecordID {
		t.Fatal("continuation duplicated a request")
	}
	s.ThreadID = "wrong"
	if _, err = Read(t.Context(), s, Cursor{}, 10); err == nil {
		t.Fatal("accepted unrelated history")
	}
}

func TestLegacyIdentityAndCounterDialect(t *testing.T) {
	s, h := fixture(t)
	s.Provider = "codex"
	u := strings.ReplaceAll(line("token_usage_record", 2, usage("turn", "resp", 110)), `"ordinal":2,`, "")
	u = strings.ReplaceAll(u, "cache_creation_input_tokens", "cache_write_input_tokens")
	write(t, s.Path, h+u)
	p, err := Read(t.Context(), s, Cursor{}, 10)
	if err != nil || len(p.Samples) != 1 || p.Samples[0].Ordinal != nil || p.Samples[0].CacheWrite == nil {
		t.Fatal(p, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = Read(ctx, s, Cursor{}, 10); err == nil {
		t.Fatal("ignored cancellation")
	}
}

// The fixtures retain only counter values and equality-preserving numeric
// aliases for native identities. They contain no dialogue, paths, or real IDs.
func TestDesensitizedAcceptanceTotals(t *testing.T) {
	for _, test := range []struct {
		name, provider string
		requests       int
		total          int64
	}{
		{"recovery", "codex", 20, 3500540},
		{"continuations", "traex", 1123, 139627052},
		{"compactions", "traex", 1233, 154617034},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", test.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Total       int64        `json:"total"`
				Compactions int          `json:"compactions"`
				Files       [][][]*int64 `json:"files"`
			}
			if err = json.Unmarshal(raw, &fixture); err != nil {
				t.Fatal(err)
			}
			src, h := fixtureSource(t, test.provider)
			for i, records := range fixture.Files {
				text := h
				for _, values := range records {
					if len(values) != 9 {
						t.Fatal("invalid anonymized record")
					}
					payload := map[string]any{"thread_id": "thread", "session_id": "session", "turn_id": fmt.Sprintf("turn-%d", *values[1]), "response_id": fmt.Sprintf("response-%d", *values[2]), "usage": map[string]any{"input_tokens": values[3], "cached_input_tokens": values[4], "cache_creation_input_tokens": values[5], "output_tokens": values[6], "reasoning_output_tokens": values[7], "total_tokens": values[8]}, "thread_token_usage": map[string]int64{"total_tokens": fixture.Total}}
					if test.provider == "codex" {
						counters := payload["usage"].(map[string]any)
						counters["cache_write_input_tokens"] = counters["cache_creation_input_tokens"]
						delete(counters, "cache_creation_input_tokens")
					}
					row := map[string]any{"type": "token_usage_record", "timestamp": "2026-01-01T00:00:00Z", "payload": payload}
					if values[0] != nil {
						row["ordinal"] = *values[0]
					}
					b, _ := json.Marshal(row)
					text += string(b) + "\n"
				}
				for j := 0; j < fixture.Compactions; j++ {
					text += line("event_msg", 0, map[string]any{"type": "item_completed", "item": map[string]string{"type": "ContextCompaction"}})
				}
				write(t, filepath.Join(filepath.Dir(src.Path), fmt.Sprintf("continuation-%d.jsonl", i)), text)
			}
			// The registered root and a continuation intentionally contain the
			// same first measurement, exercising cross-file identity preservation.
			first, _ := os.ReadFile(filepath.Join(filepath.Dir(src.Path), "continuation-0.jsonl"))
			write(t, src.Path, string(first))
			files, err := Discover(t.Context(), src)
			if err != nil {
				t.Fatal(err)
			}
			unique := map[string]int64{}
			for _, file := range files {
				cursor := Cursor{}
				for {
					page, err := Read(t.Context(), file, cursor, 37)
					if err != nil {
						t.Fatal(err)
					}
					for _, sample := range page.Samples {
						unique[sample.RecordID] = *sample.Total
					}
					cursor = page.Cursor
					if !page.More {
						break
					}
				}
			}
			var total int64
			for _, n := range unique {
				total += n
			}
			if len(unique) != test.requests || total != test.total {
				t.Fatal(len(unique), total)
			}
		})
	}
}

func fixtureSource(t *testing.T, provider string) (Source, string) {
	s, h := fixture(t)
	s.Provider = provider
	return s, h
}

func TestReplacedFileWithIdenticalMetadataIsRescanned(t *testing.T) {
	s, h := fixture(t)
	write(t, s.Path, h+line("token_usage_record", 1, usage("first", "resp", 110)))
	p, err := Read(t.Context(), s, Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	replacement := s.Path + ".replacement"
	write(t, replacement, h+line("token_usage_record", 1, usage("other", "resp", 220)))
	if err = os.Rename(replacement, s.Path); err != nil {
		t.Fatal(err)
	}
	q, err := Read(t.Context(), s, p.Cursor, 10)
	if err != nil || len(q.Samples) != 1 || *q.Samples[0].Total != 220 {
		t.Fatal(q, err)
	}
	if q.Samples[0].RecordID != p.Samples[0].RecordID || q.Samples[0].NativeContentHash == p.Samples[0].NativeContentHash {
		t.Fatal("replacement did not preserve identity and expose content conflict")
	}
}

func TestArchivedContinuationRemainsInRegisteredSession(t *testing.T) {
	s, h := fixture(t)
	write(t, s.Path, h+line("token_usage_record", 1, usage("first", "response", 110)))
	root := filepath.Dir(filepath.Dir(filepath.Dir(s.Path)))
	archive := filepath.Join(root, "archived_sessions")
	if err := os.MkdirAll(archive, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(archive, "continuation.jsonl"), h+line("token_usage_record", 2, usage("second", "response", 220)))
	files, err := Discover(t.Context(), s)
	if err != nil || len(files) != 2 {
		t.Fatal(files, err)
	}
}

func TestRegisteredChildDoesNotImportSessionAncestorsOrSiblings(t *testing.T) {
	source, header := fixture(t)
	write(t, source.Path, header)
	directory := filepath.Dir(source.Path)
	child := line("session_meta", 0, map[string]any{"id": "child", "session_id": "session", "source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]string{"parent_thread_id": "thread"}}}})
	write(t, filepath.Join(directory, "child.jsonl"), child)
	sibling := line("session_meta", 0, map[string]any{"id": "sibling", "session_id": "session", "source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]string{"parent_thread_id": "thread"}}}})
	write(t, filepath.Join(directory, "sibling.jsonl"), sibling)
	source.Path = filepath.Join(directory, "child.jsonl")
	source.ThreadID = "child"
	files, err := Discover(t.Context(), source)
	if err != nil || len(files) != 1 || files[0].ThreadID != "child" {
		t.Fatal(files, err)
	}
}
