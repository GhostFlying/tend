//go:build linux

package traex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/protocol"
)

func submittedImplementationFixture(t *testing.T, detached ...bool) (*Workers, harness.WorkerSpec, *exec.Cmd, protocol.ArtifactPayload, []byte) {
	t.Helper()
	_, spec, cmd, _ := ownedProcessFixture(t, detached...)
	spec.Role = protocol.RoleImplement
	spec.Spec = json.RawMessage(`{"deliverables":["report","files"]}`)
	uploads := 0
	spec.UploadFile = func(_ context.Context, _, name string) (protocol.ArtifactFile, error) {
		uploads++
		return protocol.ArtifactFile{FileID: fmt.Sprintf("file_original_%02d", uploads), Name: name, Size: int64(uploads), SHA256: fmt.Sprintf("%064x", uploads)}, nil
	}
	files := make([]map[string]string, 13)
	for i := range files {
		name := fmt.Sprintf("evidence-%02d.txt", i+1)
		files[i] = map[string]string{"path": name, "name": name}
	}
	arguments, err := json.Marshal(map[string]any{
		"report": protocol.ResultReport{Body: "Original completed report.\n原始结果。", Evidence: []string{"original file receipts"}, Uncertainties: []string{"deployment not performed"}},
		"files":  files,
	})
	if err != nil {
		t.Fatal(err)
	}
	results := harness.Results{Spec: spec}
	if result := results.Handle(t.Context(), harness.ToolCall{Tool: harness.ResultTool, Arguments: arguments}); !result.Success || uploads != 13 {
		t.Fatalf("original submission failed: %+v uploads=%d", result, uploads)
	}
	want := *results.Result()
	body, err := os.ReadFile(filepath.Join(spec.StateDir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Recovery receives the original upload receipts, never another upload or
	// native start. No pending native event is required to retain this result.
	spec.UploadFile = func(context.Context, string, string) (protocol.ArtifactFile, error) {
		t.Error("result recovery repeated an upload")
		return protocol.ArtifactFile{}, errors.New("must not upload")
	}
	ws := NewWorkers(WorkerOptions{Session: Options{Bin: "must-not-launch"}})
	ws.start = func(context.Context, Options) (*Session, error) {
		t.Error("result recovery started a native session")
		return nil, errors.New("must not launch")
	}
	return ws, spec, cmd, want, body
}

func TestReconcileImplementationResultPreservesOriginalSubmission(t *testing.T) {
	for _, threadKnown := range []bool{true, false} {
		t.Run(fmt.Sprintf("journal_thread_%t", threadKnown), func(t *testing.T) {
			ws, spec, _, want, original := submittedImplementationFixture(t)
			if !threadKnown {
				spec.Thread = ""
			}
			for attempt := range 2 {
				got, err := ws.ReconcileImplementationResult(t.Context(), spec)
				if err != nil || !reflect.DeepEqual(got, harness.WorkerResult{Result: &want}) {
					t.Fatalf("recovery %d changed the submitted report or thirteen receipts: %+v %v", attempt, got, err)
				}
			}
			record, err := ws.ownedWorkerProcess(spec)
			if err != nil || !record.CloseObserved || record.ThreadID != "original-thread" {
				t.Fatalf("original close was not independently recorded: %+v %v", record, err)
			}
			if err := confirmOwnedProcessStopped(record.Process); err != nil {
				t.Fatal("result returned before the original process stopped", err)
			}
			body, err := os.ReadFile(filepath.Join(spec.StateDir, "result.json"))
			if err != nil || !bytes.Equal(body, original) {
				t.Fatal("recovery rewrote original result bytes", err)
			}
		})
	}
}

func TestReconcileImplementationResultReadsFinalSubmissionAfterClose(t *testing.T) {
	ws, spec, _, want, _ := submittedImplementationFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	svc := &codexenv.Service{Root: t.TempDir(), Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	profile := codexenv.Profile{ID: "original", Home: t.TempDir(), UserHome: t.TempDir(), Enabled: true}
	if _, err := svc.Execute(ctx, codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	binding := &codexenv.Binding{Directory: svc.Root, ID: profile.ID}
	if _, err := codexenv.BindExecution(ctx, spec.StateDir, "run:"+spec.RunID, binding, false); err != nil {
		t.Fatal(err)
	}
	ref, err := binding.Pin(ctx, "run:"+spec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(ref.Directory, ".lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	type outcome struct {
		result harness.WorkerResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := ws.ReconcileImplementationResult(ctx, spec)
		done <- outcome{result, err}
	}()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			<-done
		}
	})
	// The real ownership record proves Stop passed the initial result read.
	// Its final environment confirmation is held at the existing store lock.
	for {
		record, err := ws.ownedWorkerProcess(spec)
		if err != nil {
			t.Fatal(err)
		}
		if record.CloseObserved {
			break
		}
		select {
		case early := <-done:
			joined = true
			t.Fatalf("recovery returned before the original close gate: %+v %v", early.result, early.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	want.Report.Body = "Original session's final submission.\n最后一次合法提交。"
	final, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spec.StateDir, "result.json"), final, 0600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	got := <-done
	joined = true
	if got.err != nil || !reflect.DeepEqual(got.result, harness.WorkerResult{Result: &want}) {
		t.Fatalf("recovery returned the pre-close result: %+v %v", got.result, got.err)
	}
}

func TestReconcileImplementationResultMissingDoesNotStopWorker(t *testing.T) {
	ws, spec, cmd, _ := ownedProcessFixture(t)
	for _, stateDir := range []string{spec.StateDir, ""} {
		candidate := spec
		candidate.StateDir = stateDir
		got, err := ws.ReconcileImplementationResult(t.Context(), candidate)
		if !errors.Is(err, harness.ErrNoImplementationResult) || got.Result != nil {
			t.Fatalf("absent result did not leave ordinary recovery in charge: %+v %v", got, err)
		}
	}
	if _, _, active, err := readOwnedProcess(cmd.Process.Pid); err != nil || !active {
		t.Fatal("absence of a result stopped original work", err)
	}
}

func TestReconcileImplementationResultInvalidRemainsPending(t *testing.T) {
	for _, scenario := range []string{"malformed JSON", "invalid manifest", "missing deliverables", "read error"} {
		t.Run(scenario, func(t *testing.T) {
			ws, spec, cmd, _, _ := submittedImplementationFixture(t)
			path := filepath.Join(spec.StateDir, "result.json")
			body := []byte(`{`)
			switch scenario {
			case "invalid manifest":
				body = []byte(`{"report":{"body":"original"},"files":[{"name":"evidence"}]}`)
			case "missing deliverables":
				body = []byte(`{"report":{"body":"original"}}`)
			case "read error":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "read error" {
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ws.ReconcileImplementationResult(t.Context(), spec)
			if !errors.Is(err, harness.ErrRetainedResumePending) || errors.Is(err, harness.ErrNoImplementationResult) || got.Result != nil {
				t.Fatalf("invalid result became absent or completed: %+v %v", got, err)
			}
			if scenario == "read error" {
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) {
					t.Fatal("original result IO error was lost", err)
				}
			}
			if _, _, active, err := readOwnedProcess(cmd.Process.Pid); err != nil || !active {
				t.Fatal("invalid result caused a stop before validation", err)
			}
		})
	}
}

func TestReconcileImplementationResultIdentityMismatchRemainsPending(t *testing.T) {
	for _, scenario := range []string{"run", "directory", "thread", "missing thread", "missing ownership", "pid generation"} {
		t.Run(scenario, func(t *testing.T) {
			ws, spec, cmd, _, original := submittedImplementationFixture(t)
			record, err := ws.ownedWorkerProcess(spec)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "run":
				record.RunID = "other-run"
			case "directory":
				record.Dir = t.TempDir()
			case "thread":
				record.ThreadID = "other-thread"
			case "missing thread":
				record.ThreadID = ""
			case "pid generation":
				record.Process.StartTicks += "1"
			}
			if err := saveOwnedWorkerProcess(spec, record); err != nil {
				t.Fatal(err)
			}
			if scenario == "missing ownership" {
				if err := os.Remove(filepath.Join(spec.StateDir, ownedProcessFile)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ws.ReconcileImplementationResult(t.Context(), spec)
			if !errors.Is(err, harness.ErrRetainedResumePending) || errors.Is(err, harness.ErrNoImplementationResult) || got.Result != nil {
				t.Fatalf("unbound result became completed or absent: %+v %v", got, err)
			}
			if _, _, active, err := readOwnedProcess(cmd.Process.Pid); err != nil || !active {
				t.Fatal("changed identity signalled original work", err)
			}
			body, err := os.ReadFile(filepath.Join(spec.StateDir, "result.json"))
			if err != nil || !bytes.Equal(body, original) {
				t.Fatal("ownership failure changed the submitted result", err)
			}
		})
	}
}

func TestReconcileImplementationResultDetachedWorkRemainsPending(t *testing.T) {
	ws, spec, _, _, original := submittedImplementationFixture(t, true)
	got, err := ws.ReconcileImplementationResult(t.Context(), spec)
	if !errors.Is(err, harness.ErrRetainedResumePending) || !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) || got.Result != nil {
		t.Fatalf("active detached work became a confirmed result: %+v %v", got, err)
	}
	record, err := ws.ownedWorkerProcess(spec)
	if err != nil || len(record.Detached) == 0 {
		t.Fatal("detached identities were not preserved", record, err)
	}
	for _, detached := range record.Detached {
		current, _, active, err := readOwnedProcess(detached.PID)
		if err != nil || current != detached || !active {
			t.Fatal("result recovery signalled independently running work", detached, err)
		}
	}
	body, err := os.ReadFile(filepath.Join(spec.StateDir, "result.json"))
	if err != nil || !bytes.Equal(body, original) {
		t.Fatal("pending close changed the submitted result", err)
	}
}

func TestReconcileImplementationResultLostCloseProofRemainsPending(t *testing.T) {
	ws, spec, cmd, _, original := submittedImplementationFixture(t)
	procx.KillGroup(cmd)
	_ = cmd.Wait()
	got, err := ws.ReconcileImplementationResult(t.Context(), spec)
	if !errors.Is(err, harness.ErrRetainedResumePending) || !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) || got.Result != nil {
		t.Fatalf("exited native work without close proof became a result: %+v %v", got, err)
	}
	record, err := ws.ownedWorkerProcess(spec)
	if err != nil || record.CloseObserved {
		t.Fatal("result existence manufactured historical close proof", record, err)
	}
	body, err := os.ReadFile(filepath.Join(spec.StateDir, "result.json"))
	if err != nil || !bytes.Equal(body, original) {
		t.Fatal("missing close proof changed original result bytes", err)
	}
}
