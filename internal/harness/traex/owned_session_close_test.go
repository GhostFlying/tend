package traex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

const envAcceptanceCloseServer = "TEND_TRAEX_TEST_ACCEPTANCE_CLOSE_SERVER"

// This reexecuted test binary owns an actual app-server process. It does not
// simulate Close with a socket disconnect or an in-process stop callback.
func serveAcceptanceCloseProbe() error {
	if dir := os.Getenv("TEND_TRAEX_TEST_LATE_DETACHED_DIR"); dir != "" {
		cmd := exec.Command("setsid", "sh", "-c", `echo $$ > "$1/parent"; while [ ! -e "$1/launch" ]; do sleep 0.01; done; sleep 60 </dev/null >/dev/null 2>&1 & echo $! > "$1/grandchild"`, "fixture", dir)
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	return serveAcceptanceCloseTransport(os.Stdin, os.Stdout)
}

func serveAcceptanceCloseTransport(input io.Reader, output io.Writer) error {
	decoder, encoder := json.NewDecoder(input), json.NewEncoder(output)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				if os.Getenv("TEND_TEST_ACCEPTANCE_CLOSE_ERROR") == "1" {
					return errors.New("owned app-server close failed")
				}
				return nil
			}
			return err
		}
		if len(request.ID) == 0 {
			continue
		}
		if request.Method == "" {
			if err := encoder.Encode(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "owned-thread", "turn": map[string]string{"id": "owned-turn", "status": "completed"}}}); err != nil {
				return err
			}
			continue
		}
		result := map[string]any{}
		if request.Method == methodThreadStart {
			result["thread"] = map[string]string{"id": "owned-thread"}
		}
		if request.Method == methodTurnStart {
			result["turn"] = map[string]string{"id": "owned-turn"}
		}
		if err := encoder.Encode(map[string]any{"id": request.ID, "result": result}); err != nil {
			return err
		}
		if request.Method == methodTurnStart {
			if err := encoder.Encode(map[string]any{"id": "original-verdict-call", "method": methodToolCall, "params": map[string]any{"threadId": "owned-thread", "turnId": "owned-turn", "callId": "original-verdict-call", "tool": harness.VerdictTool, "arguments": map[string]any{"verdict": "blocked", "evidence": map[string]string{"prerequisite": "absent"}, "files": []map[string]string{{"path": "checks.log", "name": "checks.log"}}}}}); err != nil {
				return err
			}
		}
	}
}

func TestOwnedAcceptanceSessionCloseAllowsOriginalVerdictRecovery(t *testing.T) {
	for _, scenario := range []string{"confirmed", "upload unknown", "close error", "missing receipt", "process recovery", "changed sealed identity", "changed native binding", "changed manifest", "other run"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			manifest := protocol.ArtifactFile{FileID: "file_original", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}
			unknown := scenario == "upload unknown"
			spec := harness.WorkerSpec{Role: protocol.RoleAccept, RunID: "original-run", StateDir: dir, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity,
				SealVerdictFiles: func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					if unknown {
						return nil, errors.New("original upload receipt is unknown")
					}
					return []protocol.ArtifactFile{manifest}, nil
				}}
			env := []string{envAcceptanceCloseServer + "=1"}
			if scenario == "close error" {
				env = append(env, "TEND_TEST_ACCEPTANCE_CLOSE_ERROR=1")
			}
			ws := NewWorkers(WorkerOptions{Session: Options{Bin: bin, Env: env}})
			w, err := ws.Start(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			for range w.Events() {
			}
			result, err := w.Wait()
			if unknown || scenario == "close error" {
				if !errors.Is(err, harness.ErrRetainedResumePending) {
					t.Fatalf("unconfirmed submission became terminal: %v", err)
				}
			} else if err != nil || result.Verdict == nil {
				t.Fatalf("owned original did not finish: %+v %v", result, err)
			}
			receipt := filepath.Join(dir, "owned-session-close.json")
			_, receiptErr := os.Stat(receipt)
			if scenario == "close error" {
				if !errors.Is(receiptErr, os.ErrNotExist) {
					t.Fatalf("failed owned Close minted a receipt: %v", receiptErr)
				}
			} else if receiptErr != nil {
				t.Fatalf("successful owned Close did not persist its exact scope: %v", receiptErr)
			}
			spec.SealVerdictFiles = func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				return []protocol.ArtifactFile{manifest}, nil
			}
			spec.Thread = "owned-thread"
			if unknown {
				altered := spec
				altered.VerdictFilesIdentity = func(context.Context, string, json.RawMessage) (string, error) { return strings.Repeat("c", 64), nil }
				changed, err := NewWorkers(WorkerOptions{}).Start(ctx, altered)
				if changed != nil || !errors.Is(err, harness.ErrRetainedResumePending) {
					t.Fatalf("changed fully sealed intent inherited original close: worker=%t err=%v", changed != nil, err)
				}
			}
			switch scenario {
			case "missing receipt":
				if err := os.Remove(receipt); err != nil {
					t.Fatal(err)
				}
				// Historical runs without independent process ownership still
				// cannot infer closure from missing native receipts.
				if err := os.Remove(filepath.Join(dir, ownedProcessFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			case "process recovery":
				if err := os.Remove(receipt); err != nil {
					t.Fatal(err)
				}
			case "changed sealed identity":
				spec.VerdictFilesIdentity = func(context.Context, string, json.RawMessage) (string, error) { return strings.Repeat("c", 64), nil }
				raw, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(strings.ReplaceAll(string(raw), strings.Repeat("b", 64), strings.Repeat("c", 64)))
				if err := os.WriteFile(filepath.Join(dir, "verdict.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed manifest":
				raw, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "verdict.json"), []byte(strings.ReplaceAll(string(raw), "file_original", "file_changed")), 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed native binding":
				raw, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "verdict.json"), []byte(strings.ReplaceAll(string(raw), "owned-turn", "another-turn")), 0o600); err != nil {
					t.Fatal(err)
				}
			case "other run":
				spec.RunID = "different-run"
			}
			reopened := NewWorkers(WorkerOptions{})
			reopened.start = func(context.Context, Options) (*Session, error) {
				t.Fatal("verdict recovery launched another native session")
				return nil, nil
			}
			reconciledUploads := 0
			spec.SealVerdictFiles = func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
				reconciledUploads++
				return []protocol.ArtifactFile{manifest}, nil
			}
			reconciled, reconcileErr := reopened.ReconcileAcceptanceVerdict(ctx, spec)
			recoverable := scenario == "confirmed" || scenario == "upload unknown" || ownedProcessSupported() && (scenario == "process recovery" || scenario == "close error")
			if !recoverable {
				if !errors.Is(reconcileErr, harness.ErrRetainedResumePending) || reconciled.Verdict != nil || reconciledUploads != 0 {
					t.Fatalf("result-only recovery bypassed original close before upload: result=%+v error=%v uploads=%d", reconciled, reconcileErr, reconciledUploads)
				}
			} else if reconcileErr != nil || reconciled.Verdict == nil || !reflect.DeepEqual(reconciled.Verdict.Files, []protocol.ArtifactFile{manifest}) {
				t.Fatalf("result-only recovery lost original evidence: %+v %v", reconciled, reconcileErr)
			}
			w, err = reopened.Start(ctx, spec)
			if !recoverable {
				if w != nil || !errors.Is(err, harness.ErrRetainedResumePending) {
					t.Fatalf("unproven close was adopted: worker=%t err=%v", w != nil, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for range w.Events() {
			}
			result, err = w.Wait()
			if err != nil || result.Verdict == nil || !reflect.DeepEqual(result.Verdict.Files, []protocol.ArtifactFile{manifest}) {
				t.Fatalf("original evidence did not reconcile: %+v %v", result, err)
			}
			if scenario == "upload unknown" {
				path := filepath.Join(dir, "verdict.json")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, change := range []struct{ name, before, after string }{{"manifest", "file_original", "file_changed"}, {"evidence", "absent", "changed"}, {"verdict", "blocked", "passed"}} {
					t.Run("late "+change.name, func(t *testing.T) {
						changed := strings.ReplaceAll(string(original), change.before, change.after)
						if changed == string(original) {
							t.Fatal("tamper fixture did not change the verdict")
						}
						if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
							t.Fatal(err)
						}
						late, err := reopened.Start(ctx, spec)
						if late != nil || !errors.Is(err, harness.ErrRetainedResumePending) {
							t.Fatalf("cached altered verdict inherited pending close proof: worker=%t err=%v", late != nil, err)
						}
						if err := os.WriteFile(path, original, 0o600); err != nil {
							t.Fatal(err)
						}
					})
				}
			}
		})
	}
}

func TestAcceptanceVerdictReconcilerWithoutFrozenInputDoesNotStart(t *testing.T) {
	ws := NewWorkers(WorkerOptions{})
	ws.start = func(context.Context, Options) (*Session, error) {
		t.Fatal("result-only reconciliation started native execution")
		return nil, nil
	}
	_, err := ws.ReconcileAcceptanceVerdict(t.Context(), harness.WorkerSpec{RunID: "original-run", Role: protocol.RoleAccept, AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, StateDir: t.TempDir(), Thread: "original-thread", SealVerdictFiles: func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
		t.Fatal("missing frozen input reached uploads")
		return nil, nil
	}})
	if !errors.Is(err, harness.ErrNoAcceptanceVerdict) {
		t.Fatalf("missing candidate became native authority: %v", err)
	}
}

func TestConnectedSessionCloseDoesNotConfirmOwnedProcess(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		dec, enc := json.NewDecoder(server), json.NewEncoder(server)
		for {
			var request map[string]json.RawMessage
			if dec.Decode(&request) != nil {
				return
			}
			if id, ok := request["id"]; ok {
				_ = enc.Encode(map[string]any{"id": id, "result": map[string]any{}})
			}
		}
	}()
	session, err := Connect(t.Context(), client, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if session.ownedSessionCloseSucceeded() {
		t.Fatal("closing a caller-owned transport minted local process close authority")
	}
}

func TestConnectedWorkerCannotMintOwnedSessionCloseReceipt(t *testing.T) {
	for _, referenceOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport close", true: "reference release"}[referenceOnly], func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			go func() { _ = serveAcceptanceCloseTransport(server, server) }()
			session, err := Connect(t.Context(), client, "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			spec := harness.WorkerSpec{Role: protocol.RoleAccept, RunID: "original-run", StateDir: t.TempDir(), AcceptanceEvidenceFiles: true, ValidateVerdictFiles: testValidateVerdictFiles, VerdictFilesIdentity: testVerdictFilesIdentity,
				SealVerdictFiles: func(context.Context, string, json.RawMessage, []harness.VerdictFileSelection) ([]protocol.ArtifactFile, error) {
					return []protocol.ArtifactFile{{FileID: "file_original", Name: "checks.log", Size: 7, SHA256: strings.Repeat("a", 64)}}, nil
				}}
			opts := WorkerOptions{}
			if referenceOnly {
				opts.Acquire = func(context.Context, harness.WorkerSpec) (*Session, func(error), error) {
					return session, func(error) {}, nil
				}
			}
			ws := NewWorkers(opts)
			ws.start = func(context.Context, Options) (*Session, error) { return session, nil }
			w, err := ws.Start(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			for range w.Events() {
			}
			_, err = w.Wait()
			if !referenceOnly && !errors.Is(err, harness.ErrRetainedResumePending) {
				t.Fatalf("transport disconnect claimed an owned close: %v", err)
			}
			if _, err := os.Stat(filepath.Join(spec.StateDir, "owned-session-close.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unowned close minted a receipt: %v", err)
			}
		})
	}
}
