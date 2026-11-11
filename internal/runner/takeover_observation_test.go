package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestTakeoverObservationPreservesOriginalAcrossUpgradeAndHandback(t *testing.T) {
	for _, version := range []int64{3, 4} {
		for _, completed := range []bool{false, true} {
			t.Run(fmt.Sprintf("protocol%d/completed=%t", version, completed), func(t *testing.T) {
				clock := testkit.NewClock()
				path := filepath.Join(t.TempDir(), "journal.db")
				j, err := journal.Open(t.Context(), path, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = j.Close() }()
				as := protocol.Assignment{Protocol: version, RunID: "original-run", TaskID: "original-task", RunToken: "original-token", Role: protocol.RoleImplement, Desired: protocol.DesiredRun}
				if err = j.AcceptAssignment(t.Context(), as, ""); err != nil {
					t.Fatal(err)
				}
				var original protocol.Envelope
				var originalObservation string
				calls := 0
				lost, cancelLost := context.WithCancel(t.Context())
				defer cancelLost()
				client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
					defer req.Body.Close()
					var env protocol.Envelope
					if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
						t.Fatal(err)
					}
					if env.Operation != protocol.OpRunsReportTakeover || env.Target != runTarget(as) || req.Header.Get(headerRun) != as.RunID {
						t.Fatal("changed observation scope", env)
					}
					pending, err := j.Requests(t.Context())
					if err != nil || len(pending) != 1 || !reflect.DeepEqual(pending[0].Envelope, env) {
						t.Fatal("observation sent before original journal commit", pending, env, err)
					}
					var payload protocol.TakeoverPayload
					if err := json.Unmarshal(env.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					calls++
					if version == 4 && (!protocol.ValidID(protocol.PrefixRequest, payload.ObservationID) || payload.ObservationID == env.RequestID) {
						t.Fatal("observation reused transport identity", env, payload)
					}
					if version == 3 && payload.ObservationID != "" {
						t.Fatal("upgraded host rewrote original protocol3 observation", payload)
					}
					if calls == 1 {
						original = env
						originalObservation = payload.ObservationID
						cancelLost()
						return nil, errors.New("server observed takeover; response lost")
					}
					mode := protocol.ControlManaged
					if calls == 2 {
						if !reflect.DeepEqual(original, env) || payload.ObservationID != originalObservation {
							t.Fatal("recovery changed original observation", original, env)
						}
					} else {
						if env.RequestID == original.RequestID || version == 4 && payload.ObservationID == originalObservation {
							t.Fatal("new takeover reused previous observation", env)
						}
						mode = protocol.ControlHumanControl
					}
					body, _ := json.Marshal(map[string]any{"task_id": as.TaskID, "control_mode": mode, "control_revision": int64(8), "lifecycle": "running"})
					return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: body}})
				})}
				cfg := Config{Journal: j, ServerURL: "http://runner.invalid", HTTPClient: client, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.DiscardHandler)}
				r := New(cfg)
				r.runnerProtocol = 4
				a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
				if completed {
					if err = r.holdCompleted(t.Context(), as, a, "actual owner activity"); !errors.Is(err, errHeld) {
						t.Fatal(err)
					}
					err = r.settleTakeover(lost, as, a)
				} else {
					err = r.reportTakeover(lost, as, a, "actual owner activity")
				}
				if err == nil || calls != 1 {
					t.Fatal("lost response was treated as success", calls, err)
				}
				if err = j.Close(); err != nil {
					t.Fatal(err)
				}
				j, err = journal.Open(t.Context(), path, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Journal = j
				cfg.IDs = testkit.NewIDGen(clock, 5)
				r = New(cfg)
				r.runnerProtocol = 4
				if err = r.settleTakeover(t.Context(), as, a); err != nil {
					t.Fatal(err)
				}
				run, err := j.Run(t.Context(), as.RunID)
				pending, pendingErr := j.Requests(t.Context())
				if err != nil || pendingErr != nil || !run.Held || run.StoppedConfirmed || len(pending) != 0 || a.desired != protocol.DesiredRun || calls != 2 {
					t.Fatal("old observation lost physical hand-back responsibility or restored server hold", run, pending, a.desired, calls, err, pendingErr)
				}
				if err = r.reportTakeover(t.Context(), as, a, "a later actual owner activity"); err != nil {
					t.Fatal(err)
				}
				run, err = j.Run(t.Context(), as.RunID)
				if err != nil || !run.Held || a.desired != protocol.DesiredHeld || calls != 3 {
					t.Fatal("independent new observation did not hold", run, a.desired, calls, err)
				}
			})
		}
	}
}

func TestTakeoverObservationUnknownAndAcceptedRemainPending(t *testing.T) {
	for _, state := range []string{"unknown", "accepted", "explicit_unknown"} {
		t.Run(state, func(t *testing.T) {
			clock := testkit.NewClock()
			j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			as := protocol.Assignment{Protocol: 4, RunID: "original-run", TaskID: "original-task"}
			if err = j.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			var sent protocol.Envelope
			client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close()
				if err := json.NewDecoder(req.Body).Decode(&sent); err != nil {
					t.Fatal(err)
				}
				if state == "explicit_unknown" {
					return managerReceiptHTTPResponse(http.StatusConflict, rpcResponse{Error: protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile original observation")})
				}
				status := protocol.OpUnknown
				if state == "accepted" {
					status = protocol.OpAccepted
				}
				return managerReceiptHTTPResponse(http.StatusOK, rpcResponse{Operation: &protocol.Operation{RequestID: sent.RequestID, Status: status}})
			})}
			r := New(Config{Journal: j, ServerURL: "http://runner.invalid", HTTPClient: client, Now: clock.Now, IDs: testkit.NewIDGen(clock, 3), Log: slog.New(slog.DiscardHandler)})
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			if err = r.reportTakeover(t.Context(), as, a, "actual observation"); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
				t.Fatal("uncertain observation was settled", err)
			}
			pending, err := j.Requests(t.Context())
			run, runErr := j.Run(t.Context(), as.RunID)
			if err != nil || runErr != nil || len(pending) != 1 || !reflect.DeepEqual(pending[0].Envelope, sent) || run.Held || a.desired != protocol.DesiredRun {
				t.Fatal("uncertainty changed original pending or execution", pending, run, a.desired, err, runErr)
			}
		})
	}
}
