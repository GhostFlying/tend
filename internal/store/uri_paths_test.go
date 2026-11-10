package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreDSNLiteralPathsPreservePools(t *testing.T) {
	for _, relative := range []bool{false, true} {
		for _, part := range []struct{ name, literal, misdirected string }{
			{"hash", "ledger#literal.db", "ledger"},
			{"query", "ledger?literal.db", "ledger"},
			{"percent", "ledger%25literal.db", "ledger%literal.db"},
		} {
			t.Run(fmt.Sprintf("%s_relative_%t", part.name, relative), func(t *testing.T) {
				if runtime.GOOS == "windows" && part.name == "query" {
					t.Skip("Windows filenames cannot contain a question mark")
				}
				root := t.TempDir()
				intended := filepath.Join(root, part.literal)
				uri := url.URL{Scheme: "file", OmitHost: true, Path: filepath.ToSlash(filepath.Join(root, part.misdirected))}
				seed, err := sql.Open("sqlite", uri.String())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := seed.ExecContext(t.Context(), "CREATE TABLE retained(value TEXT); INSERT INTO retained VALUES('unchanged')"); err != nil {
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
				write, err := sql.Open("sqlite", dsn(path, true))
				if err != nil {
					t.Fatal(err)
				}
				defer write.Close()
				assertFile := func(db *sql.DB) {
					t.Helper()
					var seq int
					var name, actual string
					if err := db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&seq, &name, &actual); err != nil {
						t.Fatal(err)
					}
					if name != "main" || filepath.Clean(actual) != intended {
						t.Fatal("pool opened another file", actual, intended)
					}
					want, err := os.Stat(intended)
					if err != nil {
						t.Fatal(err)
					}
					got, err := os.Stat(actual)
					if err != nil || !os.SameFile(want, got) {
						t.Fatal("pool identity differs", err)
					}
				}
				assertFile(write)
				if _, err := write.ExecContext(t.Context(), "CREATE TABLE pool_value(value TEXT); INSERT INTO pool_value VALUES('original')"); err != nil {
					t.Fatal(err)
				}
				read, err := sql.Open("sqlite", dsn(path, false))
				if err != nil {
					t.Fatal(err)
				}
				defer read.Close()
				assertFile(read)
				var value, mode string
				var readonly, foreignKeys int
				if err := read.QueryRowContext(t.Context(), "SELECT value FROM pool_value").Scan(&value); err != nil || value != "original" {
					t.Fatal(value, err)
				}
				if err := read.QueryRowContext(t.Context(), "PRAGMA query_only").Scan(&readonly); err != nil || readonly != 1 {
					t.Fatal("reader query_only", readonly, err)
				}
				if _, err := read.ExecContext(t.Context(), "INSERT INTO pool_value VALUES('forbidden')"); err == nil {
					t.Fatal("reader wrote to the ledger")
				}
				if err := write.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
					t.Fatal("writer foreign_keys", foreignKeys, err)
				}
				if err := write.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
					t.Fatal("writer journal mode", mode, err)
				}
				uri.RawQuery = "mode=ro"
				seed, err = sql.Open("sqlite", uri.String())
				if err != nil {
					t.Fatal(err)
				}
				defer seed.Close()
				var tables int
				if err := seed.QueryRowContext(t.Context(), "SELECT value FROM retained").Scan(&value); err != nil || value != "unchanged" {
					t.Fatal("prefix pollution was changed", value, err)
				}
				if err := seed.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil || tables != 1 {
					t.Fatal("prefix received ledger data", tables, err)
				}
			})
		}
	}
}

func TestStoreDSNPreservesNativeFilename(t *testing.T) {
	for _, writer := range []bool{false, true} {
		for _, filename := range []string{
			"/tmp/ledger#?%25.db", "../data/ledger#?%.db",
			`C:\data\ledger#%.db`, `\\server\share\ledger#%.db`, "//server/share/ledger#%.db",
		} {
			t.Run(fmt.Sprintf("%s_writer_%t", filename, writer), func(t *testing.T) {
				raw := dsn(filename, writer)
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
				if err != nil || u.Scheme != "file" || u.Host != "" || decoded != filepath.ToSlash(filename) {
					t.Fatal("DSN changed the original filename", filename, u, decoded, err)
				}
				want := url.Values{}
				want.Add("_pragma", "journal_mode(WAL)")
				want.Add("_pragma", "foreign_keys(1)")
				want.Add("_pragma", "busy_timeout(5000)")
				want.Add("_pragma", "synchronous(NORMAL)")
				if writer {
					want.Set("_txlock", "immediate")
				} else {
					want.Add("_pragma", "query_only(1)")
				}
				if u.RawQuery != want.Encode() {
					t.Fatal("DSN changed the connection options", u.RawQuery)
				}
			})
		}
	}
}
