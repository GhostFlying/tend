package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/archiveseed"
	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/testkit"
)

// The subprocess exercises main's early dispatch, including the absence of
// normal service config, network setup and server secret.
func TestOfflineArchiveCLIHelper(t *testing.T) {
	if os.Getenv("TEND_ARCHIVE_TEST_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("TEND_ARCHIVE_TEST_ARGS")), &args); err != nil {
		panic(err)
	}
	os.Args = append([]string{"tend-server"}, args...)
	main()
	os.Exit(0)
}

func TestOfflineArchiveActualCommandPipeline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	s, err := testkit.ArchiveLedger57(t.Context(), path, "../../internal/store/migrations")
	if err != nil {
		t.Fatal(err)
	}
	oldID := "historical-cli-deployment"
	if _, err = s.ExecContext(t.Context(), `INSERT INTO deployment_identity VALUES(1,?)`, oldID); err != nil {
		t.Fatal(err)
	}
	files, err := blobstore.New(path + ".files")
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("download original\x00\xff")
	object, err := files.Put(t.Context(), bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecContext(t.Context(), `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES('cli-message','owner','Archived CLI record',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecContext(t.Context(), `INSERT INTO conversation_attachments(file_id,name,media_type,size,sha256,created_at) VALUES('cli-original','original.bin','application/octet-stream',?,?,1)`, len(original), object.SHA256); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	commandDir := ""
	command := func(args ...string) ([]byte, error) {
		t.Helper()
		payload, _ := json.Marshal(args)
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestOfflineArchiveCLIHelper$")
		cmd.Dir = commandDir
		cmd.Env = append(os.Environ(), "TEND_ARCHIVE_TEST_HELPER=1", "TEND_ARCHIVE_TEST_ARGS="+string(payload), "TEND_SERVER_SECRET=")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			return nil, &cliTestError{err: err, stderr: stderr.String()}
		}
		return output, nil
	}
	root := filepath.Join(t.TempDir(), "archive")
	if _, err = command("archive", "capture", "--database", path, "--output", root); err == nil {
		t.Fatal("capture lacked stopped-writer confirmation")
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("rejected capture created output")
	}
	output, err := command("archive", "capture", "--database", path, "--output", root, "--writers-stopped")
	if err != nil {
		t.Fatal(err)
	}
	var manifest archive.Manifest
	if err = json.Unmarshal(output, &manifest); err != nil || manifest.DeploymentID != oldID {
		t.Fatal(string(output), err)
	}
	for _, args := range [][]string{{"verify"}, {"list", "--kind", "task"}, {"resources"}, {"resource", "--id", "ledger-recorded-history"}} {
		full := append([]string{"archive", args[0], "--archive", root}, args[1:]...)
		output, err = command(full...)
		if err != nil {
			t.Fatal(err)
		}
		if args[0] == "resource" && !bytes.HasPrefix(output, []byte("SQLite format 3")) {
			t.Fatal("resource download was not archived bytes")
		}
	}
	output, err = command("archive", "show", "--archive", root, "--kind", "message", "--id", "cli-message")
	if err != nil || !bytes.Contains(output, []byte("Archived CLI record")) {
		t.Fatal(string(output), err)
	}
	output, err = command("archive", "original", "--archive", root, "--kind", "attachment", "--id", "cli-original")
	if err != nil || !bytes.Equal(output, original) {
		t.Fatal("original download changed bytes", output, err)
	}
	commandDir = filepath.Dir(root)
	selection := filepath.Join(t.TempDir(), "selection.json")
	if err = os.WriteFile(selection, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = command("archive", "export-seed", "--archive", root, "--selection", selection)
	if err != nil {
		t.Fatal(err)
	}
	var seed archive.StaticSeed
	if err = json.Unmarshal(output, &seed); err != nil || seed.SourceDeploymentID != oldID || len(seed.Hosts) != 0 {
		t.Fatal(string(output), err)
	}
	if _, err = command("archive", "seed", "--archive", filepath.Base(root), "--selection", selection, "--target", filepath.Join(filepath.Base(root), "nested")); err == nil {
		t.Error("relative archive accepted nested new ledger")
	}
	if _, err = command("archive", "verify", "--archive", root); err != nil {
		t.Fatal("relative rejected target changed archive", err)
	}
	target := filepath.Join(t.TempDir(), "fresh")
	output, err = command("archive", "seed", "--archive", root, "--selection", selection, "--target", target)
	if err != nil {
		t.Fatal(err)
	}
	var receipt archiveseed.Receipt
	if err = json.Unmarshal(output, &receipt); err != nil || receipt.DeploymentID == oldID || receipt.State != "seeded_offline" {
		t.Fatal(string(output), err)
	}
	if strings.Contains(string(output), "server_secret") || strings.Contains(string(output), "runner_tokens") {
		t.Fatal("command printed credentials")
	}
	if _, err = command("archive", "seed", "--archive", root, "--selection", selection, "--target", target); err == nil {
		t.Fatal("seed overwrote existing target")
	}
	if _, err = command("archive", "show", "--archive", root, "--kind", "task", "--id", "absent"); err == nil {
		t.Fatal("missing object succeeded")
	}
	if _, err = command("archive", "original", "--archive", root, "--kind", "attachment", "--id", "../snapshot.sqlite"); err == nil {
		t.Fatal("path treated as original ID")
	}
	if _, err = command("archive", "verify", "--archive", root); err != nil {
		t.Fatal("pipeline changed archive", err)
	}
}

type cliTestError struct {
	err    error
	stderr string
}

func (e *cliTestError) Error() string { return e.err.Error() + ": " + e.stderr }
