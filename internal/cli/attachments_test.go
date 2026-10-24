package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAttachmentDownloadPublishesOnlyVerifiedOriginal(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{{"original", "original binary\x00\xff", true}, {"changed", "modified binary\x00\xff", false}, {"truncated", "part", false}} {
		t.Run(tc.name, func(t *testing.T) {
			const original = "original binary\x00\xff"
			digest := sha256.Sum256([]byte(original))
			server := newFakeServer(t)
			server.on("GET /api/v1/conversation/attachments/file_1", func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.Header.Get("Authorization") != "Bearer owner-token" {
					t.Error("owner authentication missing")
				}
				time.Sleep(5 * time.Millisecond)
				w.Header().Set("Content-Length", strconv.Itoa(len(original)))
				w.Header().Set("X-Content-SHA256", hex.EncodeToString(digest[:]))
				_, _ = io.WriteString(w, tc.body)
			})
			te := newTestEnv(t, server.srv.URL)
			te.HTTP.Timeout = time.Millisecond
			if err := te.saveConfig(config{Server: server.srv.URL}, credentials{Owner: "owner-token"}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "original.bin")
			if err := os.WriteFile(path, []byte("previous file"), 0o600); err != nil {
				t.Fatal(err)
			}
			code := Main(t.Context(), te.Env, []string{"attachment", "download", "file_1", "--out", path})
			if (code == exitOK) != tc.ok {
				t.Fatalf("code=%d error=%s", code, te.errOut)
			}
			want := "previous file"
			if tc.ok {
				want = original
				if strings.TrimSpace(te.out.String()) != path {
					t.Fatalf("path not reported: %q", te.out)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Fatalf("published bytes=%q err=%v", got, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("download temporary file leaked: %v %v", entries, err)
			}
		})
	}
}
