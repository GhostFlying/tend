package store

import (
	"strings"
	"testing"
)

func TestCanonicalOriginalLookupUsesPartialIndex(t *testing.T) {
	s, _ := openTest(t)
	rows, err := s.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+canonicalOriginalLookupQuery, bodyHash([]byte("original")))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
		if strings.Contains(detail, "SEARCH w") && strings.Contains(detail, "retained_original_windows_canonical") {
			indexed = true
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatalf("canonical lookup does not use its partial index: %v", details)
	}
}

func TestCanonicalOriginalLookupPreservesPresentationsAndMisses(t *testing.T) {
	s, _ := openTest(t)
	nativeArchiveFixture(t, s)
	source := []byte(`{ "z": 3, "text": ` + string(mustOriginalJSON(t, strings.Repeat("正文😀", 300))) + `, "a": 1 }`)
	archiveNativeChunk(t, s, "canonical-lookup", 0, append(append([]byte(`{"payload":`), source...), []byte("}\n")...))
	for _, encoding := range []string{"raw", "compact", "canonical"} {
		t.Run(encoding, func(t *testing.T) {
			raw, err := encodeOriginal(source, encoding)
			if err != nil {
				t.Fatal(err)
			}
			match, err := s.matchOriginal(t.Context(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if match == nil || match.Reference.Window == nil || match.Reference.Window.SHA256 != bodyHash(source) || match.Reference.Encoding != encoding {
				t.Fatalf("lookup did not reuse the original %s presentation: %+v", encoding, match)
			}
			restored, err := resolveOriginalReference(t.Context(), s.files, match.Reference)
			if err != nil || string(restored) != string(raw) {
				t.Fatalf("original presentation changed: err=%v", err)
			}
		})
	}
	for _, raw := range []string{`{"absent":true}`, `{"invalid"`} {
		match, err := s.matchOriginal(t.Context(), []byte(raw))
		if err != nil || match != nil {
			t.Fatalf("unmatched original became a match: %q match=%+v err=%v", raw, match, err)
		}
	}
}
