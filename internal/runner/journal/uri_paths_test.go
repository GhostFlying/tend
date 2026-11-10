package journal

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestJournalLiteralPathsUseOwnedDatabase(t *testing.T) {
	for _, relative := range []bool{false, true} {
		for _, part := range []struct{ name, literal, misdirected string }{
			{"hash", "journal#literal.db", "journal"},
			{"query", "journal?literal.db", "journal"},
			{"percent", "journal%25literal.db", "journal%literal.db"},
		} {
			t.Run(fmt.Sprintf("%s_relative_%t", part.name, relative), func(t *testing.T) {
				if runtime.GOOS == "windows" && part.name == "query" {
					t.Skip("Windows filenames cannot contain a question mark")
				}
				root := t.TempDir()
				intended := filepath.Join(root, part.literal)
				misdirected := filepath.Join(root, part.misdirected)
				uri := url.URL{Scheme: "file", OmitHost: true, Path: filepath.ToSlash(misdirected)}
				seed, err := sql.Open("sqlite", uri.String())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := seed.ExecContext(t.Context(), fmt.Sprintf("PRAGMA user_version=%d", CurrentVersion+1)); err != nil {
					_ = seed.Close()
					t.Fatal(err)
				}
				if err := seed.Close(); err != nil {
					t.Fatal(err)
				}
				path := intended
				if relative {
					cwd, err := os.Getwd()
					if err != nil {
						t.Fatal(err)
					}
					path, err = filepath.Rel(cwd, intended)
					if err != nil {
						t.Fatal(err)
					}
				}
				j, err := Open(t.Context(), path, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if j != nil {
						_ = j.Close()
					}
				})
				var seq int
				var database, actual string
				if err := j.db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&seq, &database, &actual); err != nil {
					t.Fatal(err)
				}
				owned, err := j.writer.Stat()
				if err != nil {
					t.Fatal(err)
				}
				opened, err := os.Stat(actual)
				if err != nil {
					t.Fatal(err)
				}
				if database != "main" || !os.SameFile(owned, opened) {
					t.Fatal("SQLite and writer lock selected different files", actual, path)
				}
				request := Request{Envelope: protocol.Envelope{RequestID: "original-path-request", Operation: protocol.OpRunnerClaim, Target: protocol.Target{Type: "host", ID: "host"}}}
				if err := j.PutRequest(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				generation, err := j.NextProcessGeneration(t.Context())
				if err != nil || generation != 1 {
					t.Fatal("first process generation", generation, err)
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				j, err = Open(t.Context(), path, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				requests, err := j.Requests(t.Context())
				if err != nil || len(requests) != 1 || requests[0].Envelope.RequestID != request.Envelope.RequestID {
					t.Fatal("original request was not retained", requests, err)
				}
				generation, err = j.NextProcessGeneration(t.Context())
				if err != nil || generation != 2 {
					t.Fatal("reopened process generation", generation, err)
				}
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
				uri.RawQuery = "mode=ro"
				seed, err = sql.Open("sqlite", uri.String())
				if err != nil {
					t.Fatal(err)
				}
				defer seed.Close()
				var version int64
				var tables int
				if err := seed.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
					t.Fatal(err)
				}
				if err := seed.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil {
					t.Fatal(err)
				}
				if version != CurrentVersion+1 || tables != 0 {
					t.Fatal("literal path changed the unrelated database", version, tables)
				}
			})
		}
	}
}

func TestJournalFileURIPreservesNativeFilename(t *testing.T) {
	query := url.Values{"_pragma": {"busy_timeout(5000)", "synchronous(FULL)"}, "_txlock": {"immediate"}}
	for _, filename := range []string{
		"/tmp/journal#?%25.db", "../data/journal#?%.db",
		`C:\data\journal#%.db`, `\\server\share\journal#%.db`, "//server/share/journal#%.db",
	} {
		t.Run(filename, func(t *testing.T) {
			raw := sqliteFileURI(filename, query)
			t.Logf("filename=%q URI=%q", filename, raw)
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			encoded := u.EscapedPath()
			if u.Opaque != "" {
				encoded = u.Opaque
			}
			decoded, err := url.PathUnescape(encoded)
			if err != nil || u.Scheme != "file" || u.Host != "" || decoded != filepath.ToSlash(filename) || u.RawQuery != query.Encode() {
				t.Fatal("URI changed the original filename or connection options", filename, u, decoded, err)
			}
		})
	}
}
