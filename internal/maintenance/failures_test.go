package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type lostCreationNative struct {
	*fakeNative
	loseCreation bool
}

func (n *lostCreationNative) NewThread(ctx context.Context, b protocol.MaintenanceBackend, cwd, instructions string) (NativeThread, error) {
	if n.loseCreation {
		n.created++
		return NativeThread{}, errors.New("creation receipt lost")
	}
	return n.fakeNative.NewThread(ctx, b, cwd, instructions)
}
func TestFault_MaintenanceUnknownCreationKeepsBackendFenced(t *testing.T) {
	for _, mode := range []string{"when-idle", "interrupt"} {
		t.Run(mode, func(t *testing.T) {
			c, n, cfg, opts := fixture(t)
			native := &lostCreationNative{fakeNative: n, loseCreation: true}
			c.opts.Native = native
			opts.Native = native
			command(t, c, "audit", protocol.MaintenanceRequest{})
			if err := c.Step(t.Context()); err == nil {
				t.Fatal("unknown creation hidden")
			}
			backend := cfg.Backend
			backend.Agent = "traex"
			command(t, c, "config", protocol.MaintenanceRequest{Backend: &backend, Mode: mode})
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			c.lock = nil
			d, err := Open(t.Context(), cfg, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			for range 3 {
				if err := d.Step(t.Context()); err == nil {
					t.Fatal("unknown creation retired without reconciliation")
				}
			}
			s := d.Status()
			if s.Active == nil || s.Active.SessionID != "" || s.Backend.Agent != "codex" || n.created != 1 || n.started != 0 {
				t.Fatal("backend change duplicated unknown native creation", s.Active)
			}
			command(t, d, "reconcile", protocol.MaintenanceRequest{SessionID: "original-native", Reason: "original thread identified through native history"})
			n.busy = true
			step(t, d)
			if d.Status().Active == nil {
				t.Fatal("busy original retired")
			}
			n.busy = false
			native.loseCreation = false
			step(t, d)
			step(t, d)
			step(t, d)
			step(t, d)
			if d.Status().Active.Backend.Agent != "traex" || n.created != 2 || n.started != 1 || d.Status().Active.Predecessor != nil {
				t.Fatal("reconciled idle source did not allow explicit backend adoption")
			}
		})
	}
}
func TestFault_MaintenanceFailedTurnRetainsWorkUntilExplicitAttempt(t *testing.T) {
	for _, outcome := range []string{"failed", "interrupted"} {
		for _, action := range []string{"audit", "resume", "config"} {
			t.Run(outcome+"/"+action, func(t *testing.T) {
				c, n, cfg, opts := fixture(t)
				candidate := protocol.MaintenanceCase{ID: "broken", Code: "execution_unknown", Fingerprint: "original"}
				c.upsert(candidate, 1000)
				if err := c.save(t.Context()); err != nil {
					t.Fatal(err)
				}
				step(t, c)
				step(t, c)
				original := c.Status().Active
				n.outcome, n.complete, n.busy = outcome, true, false
				step(t, c)
				s := c.Status()
				if s.Active != nil || s.Failure == nil || s.Failure.Phase != outcome || s.Failure.InputID != original.InputID || s.Failure.TurnID != original.TurnID || !slices.Contains(s.Pending, "case broken") || !strings.Contains(s.NativeError, outcome) {
					t.Fatal("failed execution lost its original facts", s)
				}
				history, _, _, err := c.History(t.Context(), 0, 10)
				if err != nil || len(history) != 1 || history[0].Phase != outcome || history[0].Outcome != outcome {
					t.Fatal(history, err)
				}
				c.upsert(candidate, 1001)
				for range 3 {
					step(t, c)
				}
				if n.started != 1 {
					t.Fatal("unchanged block retried the failed model turn")
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				c.lock = nil
				d, err := Open(t.Context(), cfg, opts)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				step(t, d)
				if d.Status().Failure == nil || n.started != 1 {
					t.Fatal("restart erased failure latch")
				}
				req := protocol.MaintenanceRequest{}
				if action == "config" {
					b := cfg.Backend
					b.Agent = "traex"
					req.Backend = &b
					req.Mode = "when-idle"
				}
				command(t, d, action, req)
				for range 3 {
					step(t, d)
				}
				s = d.Status()
				if s.Failure != nil || s.Active == nil || s.Active.InputID == original.InputID || s.Active.Predecessor != nil || strings.Contains(s.Active.Prompt, "Previous agent:") || n.started != 2 || n.created != 2 {
					t.Fatal("explicit retry lost work or injected a non-interrupt predecessor", s.Active)
				}
			})
		}
	}
}
func TestFault_MaintenancePersistenceFailureIsVisibleAndExits(t *testing.T) {
	c, n, cfg, _ := fixture(t)
	root, err := os.MkdirTemp("", "tend-persistence-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	c.cfg.ControlSocket = filepath.Join(root, "control.sock")
	token := filepath.Join(cfg.StateDir, "token")
	if err := os.WriteFile(token, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	c.opts.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	if _, err := c.db.ExecContext(t.Context(), `CREATE TRIGGER maintenance_write_fault BEFORE UPDATE ON state BEGIN SELECT RAISE(FAIL,'injected storage failure'); END;`); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"more":false}`))
	}))
	defer remote.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = c.Run(ctx, Probe{URL: remote.URL, TokenFile: token, HTTP: remote.Client()})
	if err == nil || !strings.Contains(err.Error(), "durable state unavailable") || !strings.Contains(logs.String(), "maintenance persistence failed") {
		t.Fatal("fatal persistence error did not reach process supervision", err, logs.String())
	}
	before := c.state.Revision
	w := httptest.NewRecorder()
	c.Handler(nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/maintenance/status", nil))
	var status protocol.MaintenanceStatus
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || !strings.Contains(status.NativeError, "persistence failure") || c.state.Revision != before {
		t.Fatal("read-only status hid the latched fault or tried another write", w.Body.String())
	}
	if n.created != 0 || n.started != 0 {
		t.Fatal("persistence failure dispatched native work")
	}
	if _, err := os.Stat(c.cfg.ControlSocket); !os.IsNotExist(err) {
		t.Fatal("failed service retained its listener", err)
	}
}
