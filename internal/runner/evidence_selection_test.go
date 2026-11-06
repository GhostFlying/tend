package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

type evidenceSelectionIDs struct {
	inner protocol.IDGen
	calls int
}

func (g *evidenceSelectionIDs) New(prefix protocol.Prefix) string {
	g.calls++
	return g.inner.New(prefix)
}

func TestFreshEvidenceInvalidSourcesCanBeCorrectedBeforeIntent(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "parent-file", "nul", "long-name", "symlink-loop"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			e, _ := acceptanceEvidenceFixture(t, func(w http.ResponseWriter, q *http.Request) {
				calls++
				body, _ := io.ReadAll(q.Body)
				replyEvidence(w, q.URL.Query().Get("name"), body)
			})
			ids := &evidenceSelectionIDs{inner: e.r.cfg.IDs}
			e.r.cfg.IDs = ids
			bad := "missing.log"
			switch kind {
			case "directory":
				bad = "logs"
				if err := os.Mkdir(filepath.Join(e.prepared.Dir, bad), 0o700); err != nil {
					t.Fatal(err)
				}
			case "parent-file":
				if err := os.WriteFile(filepath.Join(e.prepared.Dir, "logs"), []byte("ordinary file"), 0o600); err != nil {
					t.Fatal(err)
				}
				bad = "logs/child.log"
			case "nul":
				bad = "invalid\x00.log"
			case "long-name":
				bad = strings.Repeat("x", 300)
			case "symlink-loop":
				if runtime.GOOS == "windows" {
					t.Skip("ordinary symlink creation requires platform-specific privileges")
				}
				bad = "loop"
				if err := os.Symlink(bad, filepath.Join(e.prepared.Dir, bad)); err != nil {
					t.Fatal(err)
				}
			}
			v := harness.Verdicts{Spec: harness.WorkerSpec{RunID: e.as.RunID, Role: e.as.Role, Dir: e.prepared.Dir, StateDir: e.prepared.StateDir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: e.validate, SealVerdictFiles: e.seal, VerdictFilesIdentity: e.identity}}
			call := func(path string) harness.ToolCall {
				input := evidenceToolInput(t, []harness.VerdictFileSelection{{Path: path, Name: "check.log"}})
				return harness.ToolCall{Tool: harness.VerdictTool, ThreadID: "original-thread", TurnID: "original-turn", Arguments: input}
			}
			if result := v.Handle(t.Context(), call(bad)); result.Success {
				t.Fatal("invalid source was acknowledged")
			}
			if calls != 0 || ids.calls != 0 {
				t.Fatal("read-only selection validation allocated identity or uploaded", calls, ids.calls)
			}
			if _, err := os.Stat(filepath.Join(e.prepared.StateDir, "acceptance-evidence")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid source created an owner intent directory", err)
			}
			if _, err := os.Stat(filepath.Join(e.prepared.StateDir, "pending-verdict.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("definite source rejection left a frozen pending input", err)
			}
			if err := os.WriteFile(filepath.Join(e.prepared.Dir, "real.log"), []byte("original bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			if result := v.Handle(t.Context(), call("real.log")); !result.Success || calls != 1 || ids.calls != 1 {
				t.Fatal("fresh invalid selection could not be corrected", result.Text, calls, ids.calls)
			}
		})
	}
}

func TestEvidenceSelectionUnknownHistoryCannotBecomeFreshRejection(t *testing.T) {
	for _, kind := range []string{"intent-directory", "intent-invalid-json", "confirmed-result", "closed-session"} {
		t.Run(kind, func(t *testing.T) {
			e, _ := acceptanceEvidenceFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unknown history uploaded a source") })
			binding := "original-binding"
			intent := filepath.Join(e.prepared.StateDir, "acceptance-evidence", evidenceDigest([]byte(binding)), "intent.json")
			switch kind {
			case "intent-directory":
				if err := os.MkdirAll(intent, 0o700); err != nil {
					t.Fatal(err)
				}
			case "intent-invalid-json":
				if err := os.MkdirAll(filepath.Dir(intent), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(intent, []byte("original unreadable intent"), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				name := "verdict.json"
				if kind == "closed-session" {
					name = "owned-session-close.json"
				}
				if err := os.WriteFile(filepath.Join(e.prepared.StateDir, name), []byte("original history"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			input := evidenceToolInput(t, []harness.VerdictFileSelection{{Path: "missing.log", Name: "check.log"}})
			_, err := e.seal(t.Context(), binding, input, []harness.VerdictFileSelection{{Path: "missing.log", Name: "check.log"}})
			if err == nil || errors.Is(err, harness.ErrVerdictSelectionRejected) {
				t.Fatal("unknown original identity was relabeled as a fresh input rejection", err)
			}
		})
	}
	for _, err := range []error{os.ErrPermission, syscall.EIO, context.Canceled, context.DeadlineExceeded} {
		wrapped := &os.PathError{Op: "read", Path: "source", Err: err}
		if got := rejectFreshEvidenceSource(wrapped); errors.Is(got, harness.ErrVerdictSelectionRejected) || !errors.Is(got, err) {
			t.Fatal("permission, cancellation or read uncertainty became definite rejection", got)
		}
	}
}
