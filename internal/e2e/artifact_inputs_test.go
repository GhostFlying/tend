package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestArtifactInputsDownloadAfterSourceWorkspaceRemoval(t *testing.T) {
	srv := newServer(t, time.Minute)
	original := []byte("GIF89a\x00\xffmultiple original frames\x00")
	var mu sync.Mutex
	var sourceDir string
	var delivered protocol.ArtifactFile
	verified := make(map[protocol.RunRole]bool)
	n := newNode(t, srv, func(ws harness.WorkerSpec) testkit.WorkerScript {
		var spec domain.Spec
		if err := json.Unmarshal(ws.Spec, &spec); err != nil {
			return testkit.WorkerScript{Err: err}
		}
		result := &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Original file checked"}}
		script := testkit.WorkerScript{Result: harness.WorkerResult{Result: result}}
		if ws.Role == protocol.RoleAccept {
			script.Result = harness.WorkerResult{Verdict: &protocol.VerdictPayload{Verdict: protocol.VerdictPassed}}
		}
		script.Act = func(ctx context.Context) error {
			if spec.Goal == "produce" && ws.Role == protocol.RoleImplement {
				if err := os.MkdirAll(ws.Dir, 0o700); err != nil {
					return err
				}
				path := filepath.Join(ws.Dir, "original.gif")
				if err := os.WriteFile(path, original, 0o600); err != nil {
					return err
				}
				file, err := ws.UploadFile(ctx, path, "original.gif")
				if err != nil {
					return err
				}
				result.Files = []protocol.ArtifactFile{file}
				mu.Lock()
				sourceDir, delivered = ws.Dir, file
				mu.Unlock()
				return nil
			}
			if spec.Goal != "consume" {
				return nil
			}
			if len(spec.Inputs) != 1 {
				return fmt.Errorf("expected one frozen input, got %d", len(spec.Inputs))
			}
			path, err := ws.DownloadFile(ctx, spec.Inputs[0].FileID)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, original) {
				return fmt.Errorf("download did not preserve original: %v", err)
			}
			mu.Lock()
			verified[ws.Role] = true
			mu.Unlock()
			return nil
		}
		return script
	})
	n.start()
	eventually(t, "runner registered workspace support", func() bool {
		return srv.count(`SELECT COUNT(*) FROM hosts WHERE host_id='linux-1' AND workspace_protocol=1`) == 1
	})
	create := func(goal string, inputs []protocol.TaskInput) string {
		t.Helper()
		op := srv.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{
			"title": goal,
			"spec":  domain.Spec{Goal: goal, Acceptance: []string{"verify original file bytes"}, Deliverables: []string{domain.DeliveryReport}, Inputs: inputs, Workspace: domain.Workspace{Mode: domain.WorkspaceTemporary}},
		}, app.CreateTask{})
		var result struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(err)
		}
		return result.TaskID
	}
	source := create("produce", nil)
	eventually(t, "source completed", func() bool { return srv.task(source).Lifecycle == protocol.TaskCompleted })
	mu.Lock()
	dir, file := sourceDir, delivered
	mu.Unlock()
	if dir == "" || file.FileID == "" {
		t.Fatal("source did not upload an original")
	}
	// Only delete this test's producer workspace. The subsequent task must
	// obtain its bytes through the server, not an inherited checkout.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	target := create("consume", []protocol.TaskInput{{FileID: file.FileID}})
	eventually(t, "consumer independently accepted", func() bool { return srv.task(target).Lifecycle == protocol.TaskCompleted })
	mu.Lock()
	defer mu.Unlock()
	if !verified[protocol.RoleImplement] || !verified[protocol.RoleAccept] {
		t.Fatalf("both roles did not download originals: %+v", verified)
	}
}
