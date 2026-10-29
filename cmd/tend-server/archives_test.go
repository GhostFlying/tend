package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func emptySealedArchive(t *testing.T) string {
	t.Helper()
	clock := testkit.NewClock()
	source := filepath.Join(t.TempDir(), "old.db")
	s, err := testkit.ArchiveLedger57(t.Context(), source, "../../internal/store/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecContext(t.Context(), `INSERT INTO deployment_identity VALUES(1,'historical-deployment')`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "sealed")
	if _, err = archive.Capture(t.Context(), archive.CaptureOptions{Database: source, Destination: root, WritersStopped: true, CapturedAt: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestArchiveStaticAssemblyOpensOnceServesRepeatedReadsAndCloses(t *testing.T) {
	root := emptySealedArchive(t)
	t.Chdir(filepath.Dir(root))
	clock := testkit.NewClock()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := 0
	entries, closeArchives, err := prepareArchives(t.Context(), map[string]string{"history": "sealed"}, logger, func(ctx context.Context, path string) (*archive.Reader, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("ordinary HTTP requests must not reopen archive")
		}
		if path != root {
			t.Fatal("relative configured path was not normalized", path)
		}
		return archive.Open(ctx, path)
	})
	if err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	defer closeArchives()
	current, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "new.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	a := app.New(current, testkit.NewIDGen(clock, 910), clock.Now)
	owner := api.New(a, current, netx.Static{Identity: &netx.Identity{User: "owner"}}, bytes.Repeat([]byte{5}, 32), api.Config{Owners: []string{"owner"}, Now: clock.Now, Archives: entries}, logger)
	server := httptest.NewServer(owner.Handler())
	defer server.Close()
	login, err := http.Post(server.URL+"/api/v1/session", "application/json", bytes.NewBufferString(`{"client_id":"archive-web-client","scope":"owner"}`))
	if err != nil {
		t.Fatal(err)
	}
	var session protocol.SessionView
	err = json.NewDecoder(login.Body).Decode(&session)
	_ = login.Body.Close()
	if err != nil || login.StatusCode != 200 {
		t.Fatal(err, login.StatusCode)
	}
	for range 3 {
		for _, route := range []string{"/api/v1/archives", "/api/v1/archives/history", "/api/v1/archives/history/records/message?limit=1"} {
			request, _ := http.NewRequestWithContext(t.Context(), "GET", server.URL+route, nil)
			request.Header.Set("Authorization", "Bearer "+session.Token)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal(route, response.StatusCode)
			}
		}
	}
	server.Close()
	closeArchives()
	if calls != 1 {
		t.Fatal("refresh rescanned archive", calls)
	}
	if _, err = entries["history"].Reader.Record(t.Context(), "message", "absent"); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatal("lifecycle did not close shared reader", err)
	}
}
func TestArchiveStaticAssemblyShowsTypedFailuresAndCleansPartialInitialization(t *testing.T) {
	root := emptySealedArchive(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	missing := filepath.Join(t.TempDir(), "missing")
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "manifest.json"), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := filepath.Join(t.TempDir(), "future")
	entries, closeArchives, err := prepareArchives(t.Context(), map[string]string{"ready": root, "missing": missing, "broken": broken, "future": future}, logger, func(ctx context.Context, path string) (*archive.Reader, error) {
		if path == future {
			return nil, fmt.Errorf("actual typed boundary: %w", archive.ErrUnsupportedSchema)
		}
		return archive.Open(ctx, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeArchives()
	if entries["ready"].State != protocol.ArchiveReady || entries["missing"].Reason != "missing" || entries["broken"].Reason != "unreadable" || entries["future"].State != protocol.ArchiveUnsupported {
		t.Fatal(entries)
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("opening missing archive created it", err)
	}
	var captured *archive.Reader
	_, _, err = prepareArchives(t.Context(), map[string]string{"a-ready": root, "z-invalid": ""}, logger, func(ctx context.Context, path string) (*archive.Reader, error) {
		var err error
		captured, err = archive.Open(ctx, path)
		return captured, err
	})
	if err == nil || captured == nil {
		t.Fatal("fixture did not reach partial init", err)
	}
	if _, err = captured.Record(t.Context(), "message", "absent"); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatal("partial init leaked reader", err)
	}
}
