package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCanceledVerdictUploadPreservesIndependentSealedAudit(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "original-scope"))
	defer cancel()
	var binding string
	var input json.RawMessage
	v := Verdicts{Spec: WorkerSpec{RunID: "original-run", StateDir: t.TempDir(), AcceptanceEvidenceFiles: true,
		SealVerdictFiles: func(callCtx context.Context, b string, raw json.RawMessage, _ []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
			if callCtx != ctx {
				t.Fatal("upload received a replacement context")
			}
			binding, input = b, append(json.RawMessage(nil), raw...)
			cancel()
			return nil, callCtx.Err()
		},
		VerdictFilesIdentity: func(auditCtx context.Context, b string, raw json.RawMessage) (string, error) {
			if ctx.Err() == nil || auditCtx.Err() != nil {
				t.Fatalf("audit inherited upload cancellation: original=%v audit=%v", ctx.Err(), auditCtx.Err())
			}
			deadline, ok := auditCtx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
				t.Fatal("audit does not have the bounded cleanup deadline")
			}
			if auditCtx.Value(contextKey{}) != "original-scope" || b != binding || string(raw) != string(input) {
				t.Fatal("audit changed the original scope")
			}
			return strings.Repeat("b", 64), nil
		}}}
	got := v.Handle(ctx, ToolCall{ThreadID: "original-thread", TurnID: "original-turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{},"files":[{"path":"checks.log","name":"checks.log"}]}`)})
	if got.Success || v.Verdict() != nil || !strings.Contains(got.Text, context.Canceled.Error()) {
		t.Fatalf("canceled original upload was acknowledged or lost: %+v", got)
	}
	pending, err := v.readPendingVerdict()
	if err != nil || pending == nil || pending.SealedIdentity != strings.Repeat("b", 64) || pending.NativeBinding != binding || string(pending.Input) != string(input) {
		t.Fatalf("complete original seal audit was lost: %+v %v", pending, err)
	}
	if _, err := os.Stat(filepath.Join(v.stateDir(), "verdict.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown upload produced a final verdict: %v", err)
	}
}

func TestVerdictSealedAuditDeadlineCannotMintProof(t *testing.T) {
	originalFailure := errors.New("original upload outcome is unknown")
	v := Verdicts{Spec: WorkerSpec{RunID: "original-run", StateDir: t.TempDir(), AcceptanceEvidenceFiles: true,
		SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
			return nil, originalFailure
		},
		VerdictFilesIdentity: func(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Second {
				t.Fatal("audit has no five-second deadline")
			}
			<-ctx.Done()
			return strings.Repeat("b", 64), nil
		}}}
	got := v.Handle(t.Context(), ToolCall{ThreadID: "thread", TurnID: "turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{}}`)})
	if got.Success || !strings.Contains(got.Text, originalFailure.Error()) || !strings.Contains(got.Text, context.DeadlineExceeded.Error()) {
		t.Fatalf("deadline or original upload uncertainty was lost: %+v", got)
	}
	pending, err := v.readPendingVerdict()
	if err != nil || pending == nil || pending.SealedIdentity != "" {
		t.Fatalf("expired audit published a seal proof: %+v %v", pending, err)
	}
	if err := v.RecordOwnedSessionClose(); err == nil {
		t.Fatal("expired seal audit allowed a close receipt")
	}
}

func TestIncompleteVerdictSealedAuditCannotMintProof(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	v := Verdicts{Spec: WorkerSpec{RunID: "original-run", StateDir: t.TempDir(), AcceptanceEvidenceFiles: true,
		SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
			cancel()
			return nil, context.Canceled
		},
		VerdictFilesIdentity: func(context.Context, string, json.RawMessage) (string, error) {
			return "", errors.New("selection is only partially sealed")
		},
	}}
	got := v.Handle(ctx, ToolCall{ThreadID: "thread", TurnID: "turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{}}`)})
	if got.Success || !strings.Contains(got.Text, context.Canceled.Error()) || !strings.Contains(got.Text, "partially sealed") {
		t.Fatalf("partial seal or original cancellation was lost: %+v", got)
	}
	if err := v.RecordOwnedSessionClose(); err == nil {
		t.Fatal("partial selection allowed a close receipt")
	}
}

func TestCanceledVerdictAuditCannotReplaceChangedSealedScope(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	uploads, audits := 0, 0
	v := Verdicts{Spec: WorkerSpec{RunID: "original-run", StateDir: t.TempDir(), AcceptanceEvidenceFiles: true,
		SealVerdictFiles: func(context.Context, string, json.RawMessage, []VerdictFileSelection) ([]protocol.ArtifactFile, error) {
			uploads++
			if uploads == 2 {
				cancel()
				return nil, context.Canceled
			}
			return nil, errors.New("original upload remains unknown")
		},
		VerdictFilesIdentity: func(auditCtx context.Context, _ string, _ json.RawMessage) (string, error) {
			audits++
			if audits == 3 {
				if ctx.Err() == nil || auditCtx.Err() != nil {
					t.Fatal("post-upload audit inherited cancellation")
				}
				return strings.Repeat("c", 64), nil
			}
			return strings.Repeat("b", 64), nil
		},
	}}
	call := ToolCall{ThreadID: "thread", TurnID: "turn", Tool: VerdictTool, Arguments: json.RawMessage(`{"verdict":"blocked","evidence":{}}`)}
	if got := v.Handle(ctx, call); got.Success {
		t.Fatal("unknown original was acknowledged")
	}
	path := filepath.Join(v.stateDir(), "pending-verdict.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := v.Handle(ctx, call)
	if got.Success || !strings.Contains(got.Text, "identity changed") || !strings.Contains(got.Text, context.Canceled.Error()) {
		t.Fatalf("changed scope or original cancellation was lost: %+v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("changed audit replaced original sealed scope")
	}
	if v.Verdict() != nil {
		t.Fatal("changed audit produced a usable verdict")
	}
}
