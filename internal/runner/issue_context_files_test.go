package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueMaterialDownloadFindsOriginalOutsideAssignmentSnapshot(t *testing.T) {
	for _, fixture := range []struct {
		name string
		role protocol.RunRole
		run  string
	}{{"implement", protocol.RoleImplement, "run_impl"}, {"accept", protocol.RoleAccept, "run_accept"}, {"successor", protocol.RoleAccept, "run_successor"}} {
		t.Run(fixture.name, func(t *testing.T) {
			body := []byte("retained Issue original\x00without spec grant")
			file := protocol.IssueMaterialFile{ArtifactFile: protocol.ArtifactFile{FileID: "file_original", Name: "background.bin", Size: int64(len(body)), SHA256: evidenceDigest(body)}, Sources: []protocol.IssueMaterialSource{{Kind: "background", IssueID: "issue_original", Locator: "retained-source", Status: "current"}}}
			as := protocol.Assignment{Role: fixture.role, RunID: fixture.run, TaskID: "task_original", RunToken: "current-run-only", Spec: json.RawMessage(`{}`)}
			metadataReads, downloads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(headerRun) != as.RunID || r.Header.Get("Authorization") != "Bearer "+as.RunToken {
					t.Error("Issue original request did not use the current Run")
				}
				switch r.URL.Path {
				case "/runner/v1/files/file_original/metadata":
					metadataReads++
					_ = json.NewEncoder(w).Encode(file)
				case "/runner/v1/issue-files/file_original":
					downloads++
					_, _ = w.Write(body)
				default:
					t.Error("new Issue original used a legacy endpoint", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			r := New(Config{ServerURL: server.URL, HTTPClient: server.Client()})
			p := Prepared{ScratchDir: t.TempDir()}
			for i := 0; i < 2; i++ {
				path, err := r.downloadFile(t.Context(), as, p, file.FileID)
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(body) || filepath.Dir(path) != filepath.Join(p.ScratchDir, "issue-materials") {
					t.Fatal("original bytes were not verified and retained", path, err)
				}
			}
			if metadataReads != 2 || downloads != 2 {
				t.Fatal("download trusted a stale process-local permission cache", metadataReads, downloads)
			}
		})
	}
}

func TestIssueMaterialDownloadDoesNotDependOnQuestionReadHealth(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := []byte("ordinary Issue original")
			file := protocol.IssueMaterialFile{ArtifactFile: protocol.ArtifactFile{FileID: "file_issue", Name: "evidence.txt", Size: int64(len(body)), SHA256: evidenceDigest(body)}}
			questionReads, metadataReads, downloads := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/runner/v1/questions":
					questionReads++
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeForbidden, "question metadata unavailable")})
				case "/runner/v1/files/file_issue/metadata":
					metadataReads++
					_ = json.NewEncoder(w).Encode(file)
				case "/runner/v1/issue-files/file_issue":
					downloads++
					_, _ = w.Write(body)
				default:
					t.Error("ordinary original tried another endpoint", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			r := New(Config{ServerURL: server.URL, HTTPClient: server.Client(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			as := protocol.Assignment{Role: protocol.RoleAccept, RunID: "run_current", TaskID: "task_original", RunToken: "run-only", WorkerQuestions: true, Spec: json.RawMessage(`{}`)}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			path, err := r.downloadFile(ctx, as, Prepared{ScratchDir: t.TempDir()}, file.FileID)
			if err != nil || questionReads != 1 || metadataReads != 1 || downloads != 1 {
				t.Fatal("unrelated legacy question read blocked ordinary Issue material", path, err, questionReads, metadataReads, downloads)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(body) {
				t.Fatal(got, err)
			}
		})
	}
}

func TestIssueMaterialDownloadPreservesMetadataFailuresAndIntegrity(t *testing.T) {
	for _, scenario := range []string{"old server", "forbidden", "rate limited", "unavailable", "malformed", "wrong file", "file refused", "bad hash", "bad size"} {
		t.Run(scenario, func(t *testing.T) {
			body := []byte("original Issue bytes")
			file := protocol.IssueMaterialFile{ArtifactFile: protocol.ArtifactFile{FileID: "file_original", Name: "evidence.txt", Size: int64(len(body)), SHA256: evidenceDigest(body)}}
			reads, downloads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/metadata") {
					reads++
					switch scenario {
					case "old server":
						http.NotFound(w, r)
					case "forbidden":
						http.Error(w, "current-run-only", http.StatusForbidden)
					case "rate limited":
						http.Error(w, "current-run-only", http.StatusTooManyRequests)
					case "unavailable":
						http.Error(w, "current-run-only", http.StatusServiceUnavailable)
					case "malformed":
						_, _ = w.Write([]byte(`{`))
					case "wrong file":
						file.FileID = "other"
						_ = json.NewEncoder(w).Encode(file)
					default:
						_ = json.NewEncoder(w).Encode(file)
					}
					return
				}
				downloads++
				switch scenario {
				case "file refused":
					http.Error(w, "current-run-only", http.StatusForbidden)
				case "bad hash":
					_, _ = w.Write([]byte(strings.Repeat("x", len(body))))
				case "bad size":
					_, _ = w.Write(body[:len(body)-1])
				default:
					t.Error("unconfirmed metadata reached byte download")
				}
			}))
			defer server.Close()
			r := New(Config{ServerURL: server.URL, HTTPClient: server.Client()})
			as := protocol.Assignment{Role: protocol.RoleImplement, RunID: "run_current", RunToken: "current-run-only", Spec: json.RawMessage(`{}`)}
			p := Prepared{ScratchDir: t.TempDir()}
			_, err := r.downloadFile(t.Context(), as, p, "file_original")
			wantDownloads := 0
			if scenario == "file refused" || scenario == "bad hash" || scenario == "bad size" {
				wantDownloads = 1
			}
			if err == nil || strings.Contains(err.Error(), as.RunToken) || reads != 1 || downloads != wantDownloads {
				t.Fatal("failure was hidden, retried or promoted to success", reads, downloads, err)
			}
			entries, readErr := os.ReadDir(filepath.Join(p.ScratchDir, "issue-materials"))
			if readErr != nil && !os.IsNotExist(readErr) || len(entries) != 0 {
				t.Fatal("unverified bytes became visible", entries, readErr)
			}
		})
	}
}
