package sandbox

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/serverkit"
)

func TestRolloutPreviewThroughRealOwnerAndRunnerHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan Demo, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Data: t.TempDir(), Listener: listener, Scenario: "rollout", FastPacing: true, Ready: func(_ *serverkit.Runtime, d Demo) { ready <- d }}, slog.New(slog.NewTextHandler(testLog{t}, nil)))
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("sandbox did not stop")
		}
	}()
	var demo Demo
	select {
	case demo = <-ready:
	case err := <-done:
		done <- err
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("sandbox startup timed out")
	}
	c := webClient{t: t, ctx: ctx, url: "http://" + listener.Addr().String(), http: &http.Client{Transport: &http.Transport{}}}
	defer c.http.CloseIdleConnections()
	c.call(http.MethodPost, "/api/v1/session", protocol.SessionRequest{ClientID: "preview-test", Scope: protocol.ScopeOwner}, &c.session)
	var run protocol.RunView
	waitFor(t, ctx, "successful native history", func() bool {
		var detail protocol.TaskDetail
		c.call(http.MethodGet, "/api/v1/tasks/"+demo.Tasks[0], nil, &detail)
		for _, candidate := range detail.Runs {
			if candidate.Role == protocol.RoleImplement && candidate.Observed == protocol.RunFinished {
				run = candidate
				return true
			}
		}
		return false
	})
	var preview protocol.RolloutPreview
	c.call(http.MethodGet, "/api/v1/runs/"+run.RunID+"/preview", nil, &preview)
	if preview.State != "ready" || preview.Origin != "runner" || preview.Generation == "" || preview.Page == nil || len(preview.Page.Logs) == 0 || preview.Page.Observed != protocol.RunFinished {
		t.Fatalf("native preview: %+v", preview)
	}
	var raw protocol.RolloutPreview
	c.call(http.MethodGet, "/api/v1/runs/"+run.RunID+"/preview?generation="+preview.Generation+"&record=1", nil, &raw)
	if raw.Content == nil || !strings.Contains(raw.Content.Text, "item_started") {
		t.Fatalf("native raw record: %+v", raw)
	}
	var collected protocol.RunRolloutPage
	c.call(http.MethodGet, "/api/v1/runs/"+run.RunID+"/rollout", nil, &collected)
	if len(collected.Logs) == 0 {
		t.Fatal("collected logs disappeared")
	}
	var failure protocol.RunView
	waitFor(t, ctx, "failed native history", func() bool {
		var detail protocol.TaskDetail
		c.call(http.MethodGet, "/api/v1/tasks/"+demo.Tasks[1], nil, &detail)
		for _, candidate := range detail.Runs {
			if candidate.Observed == protocol.RunFailed {
				failure = candidate
				return true
			}
		}
		return false
	})
	c.call(http.MethodGet, "/api/v1/runs/"+failure.RunID+"/preview", nil, &preview)
	if preview.State != "ready" || preview.Page.Observed != protocol.RunFailed {
		t.Fatalf("failed native preview: %+v", preview)
	}
}
