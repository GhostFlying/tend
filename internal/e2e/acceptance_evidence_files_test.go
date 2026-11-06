package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestAcceptanceEvidenceSurvivesRealScratchCleanupAndComponentReopen(t *testing.T) {
	srv := newServer(t, time.Minute)
	original := []byte("original full check output\nFAIL exact quality gate\n\x00\xff")
	var mu sync.Mutex
	var sourceScratch string
	var manifest protocol.ArtifactFile
	verified := make(map[protocol.RunRole]bool)
	n := newNode(t, srv, func(ws harness.WorkerSpec) testkit.WorkerScript {
		var spec domain.Spec
		if err := json.Unmarshal(ws.Spec, &spec); err != nil {
			return testkit.WorkerScript{Err: err}
		}
		verdict := &protocol.VerdictPayload{}
		script := testkit.WorkerScript{Result: harness.WorkerResult{Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Independent evidence fixture"}}}}
		if ws.Role == protocol.RoleAccept {
			script.Result = harness.WorkerResult{Verdict: verdict}
		}
		script.Act = func(ctx context.Context) error {
			if spec.Goal == "consume acceptance evidence" {
				if len(spec.Inputs) != 1 {
					return fmt.Errorf("missing explicitly frozen acceptance input")
				}
				path, err := ws.DownloadFile(ctx, spec.Inputs[0].FileID)
				if err != nil {
					return err
				}
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, original) {
					return fmt.Errorf("subsequent worker lost original evidence bytes: %v", err)
				}
				mu.Lock()
				verified[ws.Role] = true
				mu.Unlock()
			}
			if ws.Role != protocol.RoleAccept {
				return nil
			}
			if !ws.AcceptanceEvidenceFiles {
				return fmt.Errorf("acceptance evidence was not negotiated")
			}
			args := map[string]any{"verdict": protocol.VerdictPassed, "evidence": map[string]any{"checked": "original bytes"}}
			if spec.Goal == "produce acceptance evidence" {
				path := filepath.Join(ws.ScratchDir, "full-check.log")
				if err := os.WriteFile(path, original, 0o600); err != nil {
					return err
				}
				args["verdict"] = protocol.VerdictBlocked
				args["files"] = []map[string]string{{"path": path, "name": "full-check.log"}}
				args["evidence"] = map[string]any{"path": path, "cause": "explicit unavailable prerequisite"}
				mu.Lock()
				sourceScratch = ws.ScratchDir
				mu.Unlock()
			}
			input, err := json.Marshal(args)
			if err != nil {
				return err
			}
			collector := harness.Verdicts{Spec: ws, StateDir: ws.StateDir, RequireBinding: true}
			collector.Bind("original-native:" + ws.RunID)
			if result := collector.Handle(ctx, harness.ToolCall{Tool: harness.VerdictTool, Arguments: input}); !result.Success {
				return fmt.Errorf("real verdict seal/upload path: %s", result.Text)
			}
			*verdict = *collector.Verdict()
			if spec.Goal == "produce acceptance evidence" {
				if len(verdict.Files) != 1 {
					return fmt.Errorf("verdict acknowledged without the selected file")
				}
				mu.Lock()
				manifest = verdict.Files[0]
				mu.Unlock()
			}
			return nil
		}
		return script
	})
	// The server commits the first verdict before its response is lost. The
	// original runner request must be reconciled before scratch cleanup.
	n.net.lose = map[string]int{protocol.OpAcceptancesSubmit: 1}
	ws := gitws.New(gitws.Config{Home: t.TempDir(), Log: quiet(), Now: srv.now})
	start := func() {
		t.Helper()
		j := n.journal()
		n.activeJournal = j
		ctx, cancel := context.WithCancel(context.Background())
		n.cancel, n.done = cancel, make(chan error, 1)
		r := runner.New(runner.Config{ServerURL: n.srv.url, HostID: "linux-1", HostToken: hostToken, Version: "test", HTTPClient: &http.Client{Transport: n.net}, Journal: j, Worker: protocol.WorkerOrca, Workers: n.workers, Workspaces: ws, Now: n.srv.now, IDs: protocol.NewULIDGen(n.srv.now, nil), Log: quiet(), RetryDelay: 20 * time.Millisecond, EventFlush: 20 * time.Millisecond})
		go func() { err := r.Run(ctx); _ = j.Close(); n.done <- err }()
	}
	create := func(goal string, inputs []protocol.TaskInput) string {
		t.Helper()
		op := n.srv.exec(owner, "tasks.create", protocol.Target{Type: "task"}, map[string]any{"title": goal, "spec": domain.Spec{Goal: goal, Acceptance: []string{"verify exact independent evidence"}, Deliverables: []string{domain.DeliveryReport}, Inputs: inputs, Workspace: domain.Workspace{Mode: domain.WorkspaceTemporary}}}, app.CreateTask{})
		var result struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(op.Result, &result); err != nil || result.TaskID == "" {
			t.Fatal("create evidence task", err)
		}
		return result.TaskID
	}
	start()
	eventually(t, "runner capability registered", func() bool {
		return srv.count(`SELECT COUNT(*) FROM host_runtime WHERE runtime_json LIKE '%acceptance-evidence-files-v1%'`) == 1
	})
	source := create("produce acceptance evidence", nil)
	eventually(t, "blocked verdict and cleanup settled", func() bool {
		if srv.count(`SELECT COUNT(*) FROM acceptances WHERE task_id=? AND verdict='blocked'`, source) != 1 {
			return false
		}
		runs, err := n.runningJournal().Runs(t.Context())
		return err == nil && len(runs) == 0
	})
	mu.Lock()
	scratch, file := sourceScratch, manifest
	mu.Unlock()
	if scratch == "" || file.FileID == "" {
		t.Fatal("worker did not explicitly select a local evidence file")
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatal("real Workspaces.Finish did not remove acceptance scratch", err)
	}
	n.stop()
	srv = srv.restart()
	n.srv = srv
	// This server fixture reloads its default app-server provider on Open.
	// Restore the original test's Orca configuration before the new Hello.
	srv.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, app.ConfigPayload{
		Repos: []app.RepoConfig{{Repo: "tend", RemoteURL: "https://example.com/tend.git"}},
		Hosts: []app.HostConfig{{HostID: "linux-1", OS: "linux", Arch: "amd64", Repos: []string{"tend"}, Slots: 2,
			TokenHash: runnerapi.HashToken(hostToken), Nodes: []string{"tag:tend-runner"}, Worker: protocol.WorkerOrca}},
	}, app.SyncConfig{})
	row, body, err := srv.app.OpenDeliverableFile(t.Context(), owner, file.FileID)
	if err != nil {
		t.Fatal("owner cannot open retained acceptance evidence after restart", err)
	}
	got, readErr := io.ReadAll(body)
	_ = body.Close()
	if readErr != nil || row.ArtifactFile != file || !bytes.Equal(got, original) {
		t.Fatal("reopened server changed original evidence", readErr)
	}
	var acceptance store.AcceptanceRow
	if err := srv.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, a, err := store.GetFinalizedAcceptanceFile(t.Context(), tx.Q(), file.FileID)
		acceptance = a
		return err
	}); err != nil || acceptance.Verdict != protocol.VerdictBlocked {
		t.Fatal("original verdict provenance changed", err)
	}
	start()
	target := create("consume acceptance evidence", []protocol.TaskInput{{FileID: file.FileID}})
	eventually(t, "both successor roles verified original evidence", func() bool { return srv.task(target).Lifecycle == protocol.TaskCompleted })
	mu.Lock()
	bothVerified := verified[protocol.RoleImplement] && verified[protocol.RoleAccept]
	mu.Unlock()
	if !bothVerified {
		t.Fatal("declared input was not independently downloaded by both roles")
	}
	if srv.count(`SELECT COUNT(*) FROM acceptances WHERE task_id=?`, source) != 1 {
		t.Fatal("lost response created a replacement verdict")
	}
	n.stop()
}
