//go:build !windows

package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBackupCLIUsesTypedControlAndSavedPreview(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "updater.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	seen := []string{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		var input map[string]json.RawMessage
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
		}
		switch r.URL.Path {
		case "/backup/prune":
			var got protocol.BackupPruneRequest
			if err := json.Unmarshal(input["backup_prune"], &got); err != nil || got.Preview.SHA256 != "retained-preview" {
				t.Error("prune did not preserve exact preview", got, err)
			}
		case "/backup/verify":
			var got protocol.BackupVerification
			if err := json.Unmarshal(input["backup_verification"], &got); err != nil || got.CheckpointID != "backups/original" {
				t.Error(got, err)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]string{"status": "ok"}})
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	for _, action := range []string{"status", "preview", "verify", "prune", "protect-log"} {
		var output, errors bytes.Buffer
		data := ""
		args := []string{"updater", "backup", action, "--socket", socket, "--json"}
		if action == "protect-log" {
			data = `{"log_id":"owned-launch","expected_generation":2,"reason":"incident"}`
			args = append(args, "--file", "-")
		}
		if action == "verify" {
			data = `{"checkpoint_id":"backups/original","expected_generation":2,"manifest_sha256":"frozen-manifest"}`
			args = append(args, "--file", "-")
		}
		if action == "prune" {
			data = `{"format":1,"sha256":"retained-preview"}`
			args = append(args, "--file", "-")
		}
		env := &Env{Stdin: strings.NewReader(data), Stdout: &output, Stderr: &errors, Getenv: func(string) string { return "" }}
		if code := Main(t.Context(), env, args); code != 0 || !strings.Contains(output.String(), "ok") {
			t.Fatalf("%s: exit=%d output=%s error=%s", action, code, output.String(), errors.String())
		}
	}
	if len(seen) != 5 || seen[0] != "GET /backup/status" || seen[3] != "POST /backup/prune" {
		t.Fatal(seen)
	}
}
