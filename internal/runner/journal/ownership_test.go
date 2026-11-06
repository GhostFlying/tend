package journal

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestJournalRejectsConcurrentProcessWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := Open(t.Context(), path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	for _, alias := range []string{"original", "symbolic", "hard"} {
		t.Run(alias, func(t *testing.T) {
			candidate := path
			if alias != "original" {
				candidate = filepath.Join(filepath.Dir(path), alias+".db")
				link := os.Link
				if alias == "symbolic" {
					link = os.Symlink
				}
				if err := link(path, candidate); err != nil {
					t.Skipf("filesystem does not support %s links: %v", alias, err)
				}
			}
			cmd := journalOwnershipHelper(t, candidate, "conflict")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("second process admitted to the journal: %v\n%s", err, output)
			}
		})
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if output, err := journalOwnershipHelper(t, path, "available").CombinedOutput(); err != nil {
		t.Fatalf("journal remained locked after close: %v\n%s", err, output)
	}
}

func TestJournalProcessExitReleasesOwnershipAndPreservesRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	cmd := journalOwnershipHelper(t, path, "hold")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() || reader.Text() != "ownership held" {
		t.Fatalf("child did not acquire ownership: %q %v", reader.Text(), reader.Err())
	}
	if j, err := Open(t.Context(), path, time.Now); err == nil {
		_ = j.Close()
		t.Fatal("parent opened the live child writer's journal")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	j, err := Open(t.Context(), path, time.Now)
	if err != nil {
		t.Fatalf("confirmed process exit did not release journal ownership: %v", err)
	}
	defer j.Close()
	requests, err := j.Requests(t.Context())
	if err != nil || len(requests) != 1 || requests[0].Envelope.RequestID != "original-request" {
		t.Fatalf("process replacement lost original request: %+v %v", requests, err)
	}
}

func journalOwnershipHelper(t *testing.T, path, mode string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestJournalOwnershipProcessHelper$", "-test.count=1", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "TEND_TEST_JOURNAL_OWNERSHIP_PATH="+path, "TEND_TEST_JOURNAL_OWNERSHIP_MODE="+mode)
	return cmd
}

func TestJournalOwnershipProcessHelper(t *testing.T) {
	path, mode := os.Getenv("TEND_TEST_JOURNAL_OWNERSHIP_PATH"), os.Getenv("TEND_TEST_JOURNAL_OWNERSHIP_MODE")
	if path == "" {
		return
	}
	j, err := Open(t.Context(), path, time.Now)
	if mode == "conflict" {
		if err == nil {
			_ = j.Close()
			t.Fatal("concurrent writer was accepted")
		}
		if !strings.Contains(err.Error(), "writer ownership") {
			t.Fatalf("open failed for a different reason: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if mode == "hold" {
		request := Request{Envelope: protocol.Envelope{RequestID: "original-request", Operation: protocol.OpRunnerClaim}}
		if err := j.PutRequest(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		fmt.Println("ownership held")
		<-t.Context().Done()
	}
}
