package maintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestMaintenanceProgramCheckDoesNotCallModelAndRearmsRecoveredFault(t *testing.T) {
	c, n, cfg, _ := fixture(t)
	token := filepath.Join(cfg.StateDir, "token")
	if err := os.WriteFile(token, []byte("synthetic-owner"), 0600); err != nil {
		t.Fatal(err)
	}
	healthy := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-owner" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(200)
		case "/api/v1/issues":
			v := protocol.IssueSummary{}
			v.IssueID = "sample"
			v.State = protocol.IssueActive
			_ = json.NewEncoder(w).Encode(protocol.IssuePage{Items: []protocol.IssueSummary{v}})
		case "/api/v1/issues/sample":
			v := protocol.IssueView{}
			v.IssueID = "sample"
			v.State = protocol.IssueActive
			status := "exhausted"
			if healthy {
				status = "waiting"
			}
			v.Manager = &protocol.IssueManagerView{Recovery: &protocol.AgentRecoveryView{Status: status}}
			_ = json.NewEncoder(w).Encode(v)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := Probe{URL: srv.URL, TokenFile: token, HTTP: srv.Client()}
	if err := c.Check(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if n.created != 0 || n.started != 0 || len(c.Status().Cases) != 1 {
		t.Fatal("program check ran a model or missed failure")
	}
	command(t, c, "resolve", protocol.MaintenanceRequest{CaseID: "issue-sample", Reason: "Original recovery verified"})
	if err := c.Check(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if c.Status().Cases[0].Status != "resolved" {
		t.Fatal("unchanged evidence repeated the same diagnosis")
	}
	healthy = true
	if err := c.Check(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	healthy = false
	if err := c.Check(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if c.Status().Cases[0].Status != "open" {
		t.Fatal("recurring fault remained suppressed after recovery")
	}
}

func TestMaintenanceInterruptRequestAfterNaturalCompletionHasNoPredecessor(t *testing.T) {
	c, n, _, _ := fixture(t)
	launch(t, c)
	b := c.Status().Backend
	b.Agent = "traex"
	command(t, c, "config", protocol.MaintenanceRequest{Backend: &b, Mode: "interrupt"})
	n.busy = false
	n.complete = true
	step(t, c)
	if n.interrupted != 0 || c.Status().Predecessor != nil {
		t.Fatal("natural completion was recorded as interrupted")
	}
}

func TestMaintenanceTaskFactsExcludeNormalWaitAndPausedWork(t *testing.T) {
	for _, state := range protocol.ExecutionStateValues() {
		v := protocol.IssueView{}
		v.State = protocol.IssueActive
		v.Tasks = []protocol.IssueTaskSummary{{TaskID: "task", Lifecycle: protocol.TaskRunning, ControlMode: protocol.ControlManaged, Execution: &protocol.TaskExecutionView{State: state}}}
		if got := len(DetectIssue(v)); (got > 0) != (state == protocol.ExecutionUnknown) {
			t.Errorf("%s produced %d candidates", state, got)
		}
		v.Tasks[0].ControlMode = protocol.ControlPaused
		if len(DetectIssue(v)) != 0 {
			t.Fatal("paused work produced candidate")
		}
	}
}
