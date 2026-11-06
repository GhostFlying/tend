package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

func boundedCommandInputs(t *testing.T) ([]string, protocol.BoundedMaintenanceAuthorization) {
	t.Helper()
	root := t.TempDir()
	config, plan, authorization := filepath.Join(root, "config.json"), filepath.Join(root, "plan.json"), filepath.Join(root, "authorization.json")
	if err := os.WriteFile(config, []byte(`{"instance_id":"original","role":"server"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, []byte(`{"original_plan":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", ExpectedGeneration: 7, ExpectedPhase: "draining", PlanSHA256: strings.Repeat("a", 64), Role: "server", Reason: "Retain original unknown work", ExpiresAtMillis: 9999999999999, Timeouts: protocol.DefaultMaintenanceTimeoutPolicy(), ForceOnDrainTimeout: true}
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorization, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return []string{"--config", config, "--plan", plan, "--authorization", authorization, "--json"}, auth
}

func TestBoundedInstallerDispatchesStableAuthorityAndRealFailure(t *testing.T) {
	args, auth := boundedCommandInputs(t)
	for _, action := range []string{"plan", "apply", "force", "extend", "cancel", "takeover"} {
		t.Run(action, func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			failure := errors.New("original stop remains unknown")
			err := runBoundedWith(t.Context(), append([]string{action}, args...), &out,
				func(ctx context.Context, cfg updater.Config, gotAction, planFile, authorizationFile, request string, generation uint64, phase string) (any, error) {
					calls++
					if gotAction != action || cfg.InstanceID != "original" || planFile != args[3] || authorizationFile != args[5] || request != auth.RequestID || generation != auth.ExpectedGeneration || phase != auth.ExpectedPhase {
						t.Error("installer dispatch lost original input", gotAction, cfg, planFile, authorizationFile, request, generation, phase)
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Error("installer dispatch has no command deadline")
					}
					return map[string]any{"outcome": "unknown", "drained": false, "request_id": request}, failure
				})
			if !errors.Is(err, failure) || calls != 1 || !strings.Contains(out.String(), `"drained":false`) || !strings.Contains(out.String(), auth.RequestID) {
				t.Fatal("installer failure was converted to success or retried", calls, err, out.String())
			}
		})
	}
}

func TestBoundedInstallerReadDoesNotRequireControlSocketOrAuthorization(t *testing.T) {
	args, _ := boundedCommandInputs(t)
	for _, action := range []string{"status", "reconcile"} {
		var out bytes.Buffer
		calls := 0
		err := runBoundedWith(t.Context(), []string{action, "--config", args[1], "--request-id", "req_01M6F01JSS6Q9BFV6DJNSSBHEY"}, &out,
			func(_ context.Context, _ updater.Config, gotAction, plan, auth, request string, generation uint64, phase string) (any, error) {
				calls++
				if gotAction != action || plan != "" || auth != "" || generation != 0 || phase != "" || request == "" {
					t.Error("read path required mutation authority")
				}
				return map[string]any{"outcome": "timed_out", "unknown": true}, nil
			})
		if err != nil || calls != 1 || !strings.Contains(out.String(), "timed_out") {
			t.Fatal(err, calls, out.String())
		}
	}
}

func TestBoundedInstallerRejectsConflictingCASBeforeExecution(t *testing.T) {
	args, _ := boundedCommandInputs(t)
	for _, conflict := range [][]string{{"--request-id", "req_01M6F01JSS6Q9BFV6DJNSSBHEZ"}, {"--expected-generation", "0"}, {"--expected-phase", "other"}, {"--timeout", "0s"}} {
		called := false
		argv := append(append([]string{"force"}, args...), conflict...)
		err := runBoundedWith(t.Context(), argv, &bytes.Buffer{}, func(context.Context, updater.Config, string, string, string, string, uint64, string) (any, error) {
			called = true
			return nil, nil
		})
		if err == nil || called {
			t.Fatal("conflicting authorization reached installer", conflict, err, called)
		}
	}
}

func TestBoundedInstallerAcceptsExplicitLegacyZeroGeneration(t *testing.T) {
	args, auth := boundedCommandInputs(t)
	auth.ExpectedGeneration = 0
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[5], raw, 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = runBoundedWith(t.Context(), append(append([]string{"force"}, args...), "--expected-generation", "0"), &bytes.Buffer{},
		func(_ context.Context, _ updater.Config, _, _, _, request string, generation uint64, _ string) (any, error) {
			called = true
			if generation != 0 || request != auth.RequestID {
				t.Error("legacy generation or request changed", generation, request)
			}
			return map[string]any{"outcome": "unknown"}, nil
		})
	if err != nil || !called {
		t.Fatal("explicit legacy zero was rejected", err, called)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "expected_generation")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(args[5], raw, 0600); err != nil {
		t.Fatal(err)
	}
	called = false
	err = runBoundedWith(t.Context(), append([]string{"force"}, args...), &bytes.Buffer{},
		func(context.Context, updater.Config, string, string, string, string, uint64, string) (any, error) {
			called = true
			return map[string]any{}, nil
		})
	if err == nil || called {
		t.Fatal("missing generation was silently treated as zero", err, called)
	}
}

func TestBoundedInstallerDeadlinePreservesUnknownWithoutSecondAttempt(t *testing.T) {
	args, auth := boundedCommandInputs(t)
	var out bytes.Buffer
	calls := 0
	start := time.Now()
	err := runBoundedWith(t.Context(), append(append([]string{"force"}, args...), "--timeout", "20ms"), &out,
		func(ctx context.Context, _ updater.Config, _, _, _, _ string, _ uint64, _ string) (any, error) {
			calls++
			<-ctx.Done()
			return nil, ctx.Err()
		})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || time.Since(start) > time.Second || !strings.Contains(out.String(), `"outcome":"unknown"`) || !strings.Contains(out.String(), auth.RequestID) {
		t.Fatal("deadline lost original uncertain action or retried", err, calls, out.String())
	}
}

func TestBoundedInstallerNativeStatusReadsOriginalDurableFacts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native bounded installer is Linux-specific")
	}
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	stateFile := filepath.Join(root, "state.json")
	want := updater.State{Format: 4, InstanceID: "original", Generation: 7,
		Bounded: &protocol.BoundedMaintenanceState{
			Authorization: protocol.BoundedMaintenanceAuthorization{RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEY", ExpectedGeneration: 7, ExpectedPhase: "draining"},
			Phase:         "draining", PhaseEnteredAtMillis: 100, PhaseDeadlineMillis: 160, OverallStartedAtMillis: 90, OverallDeadlineMillis: 450, Outcome: "timed_out",
			Audit: []protocol.BoundedMaintenanceAudit{{Action: "force", Phase: "draining", Outcome: "unknown", Reason: "Retain original work", Archive: &protocol.ArchiveOnlineStatus{HostedDrained: false, Blockers: []string{"old_work_unknown"}}}},
		}}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(updater.Config{Role: "server", InstanceID: "original", StateDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, cfg, 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"status", "reconcile"} {
		var out bytes.Buffer
		if err := run(t.Context(), []string{"bounded", action, "--config", config, "--timeout", "1s", "--json"}, &out); err != nil {
			t.Fatal("real native installer entry did not read durable state", action, err, out.String())
		}
		var got updater.State
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("native status changed original unknown/deadlines/audit: %+v, %v", got, err)
		}
		current, err := os.ReadFile(stateFile)
		if err != nil || !bytes.Equal(current, raw) {
			t.Fatal("native read changed the original durable state", string(current), err)
		}
	}
}
