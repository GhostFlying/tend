package nativeusage

import (
	"os"
	"strings"
	"testing"
)

func TestHeaderCacheDetectsRewrittenMetadataWithRestoredMtime(t *testing.T) {
	src, h := fixture(t)
	write(t, src.Path, h)
	index := NewDiscovery()
	page, err := index.Read(t.Context(), src, Cursor{}, 256, false)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src.Path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.ReplaceAll(h, "thread", "others")
	write(t, src.Path, changed)
	if err := os.Chtimes(src.Path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Read(t.Context(), src, page.Cursor, 256, false); err == nil {
		t.Fatal("cached header hid changed source identity")
	}
}
func TestCachedReaderStillChecksPartialLineAndTruncation(t *testing.T) {
	src, h := fixture(t)
	body := line("token_usage_record", 1, usage("turn", "response", 110))
	write(t, src.Path, h+body[:len(body)-1])
	index := NewDiscovery()
	page, err := index.Read(t.Context(), src, Cursor{}, 256, false)
	if err != nil || !page.Incomplete || len(page.Samples) != 0 {
		t.Fatal(page, err)
	}
	write(t, src.Path, h+body)
	page, err = index.Read(t.Context(), src, page.Cursor, 256, false)
	if err != nil || page.Incomplete || len(page.Samples) != 1 {
		t.Fatal(page, err)
	}
	write(t, src.Path, h)
	page, err = index.Read(t.Context(), src, page.Cursor, 256, false)
	if err != nil || page.Cursor.Offset > int64(len(h)) {
		t.Fatal(page, err)
	}
}
