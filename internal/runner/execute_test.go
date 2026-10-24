package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestOutcomeSaysWhatTheRunReported(t *testing.T) {
	for _, c := range []struct {
		op, payload, want string
	}{
		{protocol.OpAcceptancesSubmit, `{"verdict":"passed"}`, "[verdict passed]"},
		{protocol.OpRunsRelease, `{"reason":"no_changes"}`, "[reason no_changes]"},
		{protocol.OpRunsSubmitArtifact, `{"mounts":[{"path":"a","changed":true},{"path":"b"},{"path":"c","changed":true}]}`, "[changed a,c]"},
	} {
		got := fmt.Sprint(outcome(protocol.Envelope{Operation: c.op, Payload: json.RawMessage(c.payload)}))
		if got != c.want {
			t.Errorf("outcome(%s %s) = %s, want %s", c.op, c.payload, got, c.want)
		}
	}
}

// directoryReportWorkspace returns shared prepared facts so the test also
// detects accidental clearing of the persisted/prepared physical directory.
type directoryReportWorkspace struct {
	completionWorkspaces
	report protocol.WorkspaceReport
}

func (w directoryReportWorkspace) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	return Prepared{Report: w.report}, nil
}

func TestWorkspaceDirectoryReportNegotiationAndRetry(t *testing.T) {
	for _, negotiated := range []bool{false, true} {
		t.Run(fmt.Sprint(negotiated), func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{RunID: "run_directory", Role: protocol.RoleImplement, MountDirectories: negotiated}
			if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
				t.Fatal(err)
			}
			var reports []protocol.Envelope
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
					t.Error(err)
				}
				if env.Operation == protocol.OpRunsReportWorkspace {
					reports = append(reports, env)
					if !negotiated {
						// This is the former server report shape, including its
						// strict rejection of unknown fields inside mounts.
						var oldReport struct {
							Mounts []struct {
								Path            string `json:"path"`
								ResolvedBaseSHA string `json:"resolved_base_sha"`
								ResolvedFrom    string `json:"resolved_from"`
								Branch          string `json:"branch"`
							} `json:"mounts"`
						}
						decoder := json.NewDecoder(bytes.NewReader(env.Payload))
						decoder.DisallowUnknownFields()
						if err := decoder.Decode(&oldReport); err != nil {
							t.Errorf("old server rejects unnegotiated field: %v", err)
						}
					} else {
						var report protocol.WorkspaceReport
						if err := json.Unmarshal(env.Payload, &report); err != nil || len(report.Mounts) != 1 || report.Mounts[0].Directory != "/actual/worktree" {
							t.Errorf("negotiated directory absent: %s %v", env.Payload, err)
						}
					}
					if len(reports) == 1 {
						_, _ = w.Write([]byte("lost receipt"))
						return
					}
				}
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer srv.Close()
			workspace := directoryReportWorkspace{report: protocol.WorkspaceReport{Mounts: []protocol.ResolvedMount{{Path: "repo", Directory: "/actual/worktree", Branch: "feature", ResolvedBaseSHA: "base", ResolvedFrom: "commit"}}}}
			r := New(Config{Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Workspaces: workspace, ServerURL: srv.URL, HTTPClient: srv.Client(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RetryDelay: time.Nanosecond})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := r.prepare(ctx, as); err != nil {
				t.Fatal(err)
			}
			if len(reports) != 2 || reports[0].RequestID != reports[1].RequestID || !bytes.Equal(reports[0].Payload, reports[1].Payload) {
				t.Fatalf("retry changed report identity: %+v", reports)
			}
			if workspace.report.Mounts[0].Directory != "/actual/worktree" {
				t.Fatal("wire compatibility erased the physical-directory fact")
			}
		})
	}
}
