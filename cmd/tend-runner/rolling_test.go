package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRollingAuthorizationReadsOnlyTheSupervisedJournalOnRequest(t *testing.T) {
	for _, scenario := range []struct {
		name, journalID, extra string
		offerProof, stopped    bool
		staleControl           bool
		spooled                bool
		withPoll, pollSettled  bool
		want                   bool
	}{
		{name: "legacy journal", offerProof: true, stopped: true, want: true},
		{name: "independent journal", journalID: "installed-generation", offerProof: true, stopped: true, want: true},
		{name: "spooled after reopen", offerProof: true, stopped: true, spooled: true, want: true},
		{name: "active native poll", journalID: "installed-generation", offerProof: true, stopped: true, withPoll: true, want: true},
		{name: "poll settled after heartbeat", journalID: "installed-generation", offerProof: true, stopped: true, pollSettled: true},
		{name: "native poll on original journal", offerProof: true, stopped: true, withPoll: true},
		{name: "older server", stopped: true},
		{name: "unconfirmed claim", offerProof: true, stopped: true, extra: protocol.OpRunnerClaim},
		{name: "workspace cleanup", offerProof: true, stopped: true, extra: protocol.OpWorkspaceCleanup},
		{name: "unknown operation", offerProof: true, stopped: true, extra: "original.unclassified"},
		{name: "live native execution", offerProof: true},
		{name: "stale persisted maintenance", offerProof: true, stopped: true, staleControl: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			clock := testkit.NewClock()
			ids := testkit.NewIDGen(clock, 1)
			home := t.TempDir()
			path := filepath.Join(home, "runner", "journal.db")
			if scenario.journalID != "" {
				path = filepath.Join(home, "runner", "generations", scenario.journalID, "journal.db")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			writer, err := journal.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { writer.Close() })
			as := protocol.Assignment{Role: protocol.RoleIssueManager, Protocol: protocol.MinimumRunnerProtocol, RunID: ids.New(protocol.PrefixRun), LeaseID: ids.New(protocol.PrefixLease), RunToken: "private-run-token"}
			if err := writer.AcceptAssignment(ctx, as, ""); err != nil {
				t.Fatal(err)
			}
			if scenario.stopped {
				if err := writer.ConfirmStopped(ctx, as.RunID); err != nil {
					t.Fatal(err)
				}
			}
			commandID, requestID := ids.New(protocol.PrefixRequest), ids.New(protocol.PrefixRequest)
			errorText := "private-error"
			if scenario.spooled {
				errorText = strings.Repeat(errorText, 1024)
			}
			reply, err := json.Marshal(protocol.ManagerNativeReply{ID: commandID, Result: json.RawMessage(`"private-output"`), Error: errorText})
			if err != nil {
				t.Fatal(err)
			}
			original := journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: requestID, Operation: protocol.OpManagerNativeReply, Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: reply}}
			if err := writer.PutRequest(ctx, original); err != nil {
				t.Fatal(err)
			}
			if scenario.spooled {
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				writer, err = journal.Open(ctx, path, clock.Now)
				if err != nil {
					t.Fatal(err)
				}
			}
			saved, err := writer.RequestByID(ctx, requestID)
			if err != nil || len(saved) != 1 {
				t.Fatal("missing original receipt", err)
			}
			count := int64(1)
			if scenario.extra != "" {
				count++
				if err := writer.PutRequest(ctx, journal.Request{Envelope: protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: scenario.extra}}); err != nil {
					t.Fatal(err)
				}
			}
			proof := &protocol.RollingPendingProof{Sequence: 14, JournalID: scenario.journalID, Operations: []protocol.RollingPendingOperation{{Operation: protocol.OpManagerNativeReply, RunID: as.RunID, LeaseID: as.LeaseID, CommandID: commandID, RequestID: requestID}}}
			if scenario.withPoll || scenario.pollSettled {
				count++
			}
			if scenario.withPoll {
				idle := protocol.Assignment{Role: protocol.RoleIssueManager, Protocol: protocol.MinimumRunnerProtocol, RunID: ids.New(protocol.PrefixRun), LeaseID: ids.New(protocol.PrefixLease), RunToken: "private-idle-token"}
				if err := writer.AcceptAssignment(ctx, idle, ""); err != nil {
					t.Fatal(err)
				}
				if err := writer.SetPhase(ctx, idle.RunID, journal.PhaseStarted); err != nil {
					t.Fatal(err)
				}
				poll := ids.New(protocol.PrefixRequest)
				if err := writer.PutRequest(ctx, journal.Request{RunID: idle.RunID, Envelope: protocol.Envelope{RequestID: poll, Operation: protocol.OpManagerNativeClaim, Target: protocol.Target{Type: "run", ID: idle.RunID}, Payload: json.RawMessage(`{}`)}}); err != nil {
					t.Fatal(err)
				}
				proof.Operations = append(proof.Operations, protocol.RollingPendingOperation{Operation: protocol.OpManagerNativeClaim, RunID: idle.RunID, LeaseID: idle.LeaseID, RequestID: poll})
			}
			binding := protocol.UpgradeBinding{InstanceID: "host", UpgradeID: "release", ProcessInstanceID: "next"}
			auth := protocol.RollingAuthorization{Revision: 9, Request: protocol.RollingRunnerGrantRequest{Binding: binding}, PredecessorJournalID: &scenario.journalID}
			control := protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: auth.Revision, UpgradeID: binding.UpgradeID}
			if scenario.staleControl {
				control.Revision--
			}
			if err := writer.SaveControl(ctx, control); err != nil {
				t.Fatal(err)
			}
			requests := make(chan protocol.RollingRunnerGrantRequest, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var env protocol.Envelope
				err := json.NewDecoder(r.Body).Decode(&env)
				if err != nil || env.Operation != "runner.rolling_authorize" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != auth.Revision || r.Header.Get("Authorization") != "Bearer host-token" {
					t.Errorf("invalid authorization RPC: operation=%q revision=%v err=%v", env.Operation, env.ExpectedControlRevision, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var req protocol.RollingRunnerGrantRequest
				decoder := json.NewDecoder(bytes.NewReader(env.Payload))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&req); err != nil || req.Binding != binding {
					t.Errorf("rolling authorization changed binding: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- req
				g := protocol.RollingRunnerGrant{UpgradeGrant: protocol.UpgradeGrant{Binding: binding}}
				if req.PendingProof == nil && scenario.offerProof {
					g.PendingCheck = &protocol.RollingPendingCheck{Sequence: proof.Sequence, Count: count}
				}
				g.Allowed = reflect.DeepEqual(req.PendingProof, proof)
				data, _ := json.Marshal(g)
				_ = json.NewEncoder(w).Encode(map[string]any{"operation": protocol.Operation{Status: protocol.OpSucceeded, RequestID: env.RequestID, Result: data}})
			}))
			t.Cleanup(srv.Close)
			t.Setenv("TEND_HOST_TOKEN", "host-token")
			cfg := filepath.Join(home, "runner.json")
			configJSON, _ := json.Marshal(map[string]string{"home": home, "host_id": "host", "server_url": srv.URL, "updater_control_socket": filepath.Join(home, "control.sock")})
			if err := os.WriteFile(cfg, configJSON, 0o600); err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(auth)
			var out bytes.Buffer
			err = rollingCommand(ctx, "rolling-authorize", []string{"--config", cfg}, bytes.NewReader(input), &out)
			var grant protocol.UpgradeGrant
			if err != nil || json.Unmarshal(out.Bytes(), &grant) != nil || grant.Allowed != scenario.want {
				t.Fatalf("authorization = %+v (%s), err=%v", grant, out.String(), err)
			}
			calls := 1
			if scenario.want {
				calls++
			}
			if len(requests) != calls || (<-requests).PendingProof != nil {
				t.Fatal("initial request must use the historical wire shape")
			}
			if rows, err := writer.RequestByID(ctx, requestID); err != nil || !reflect.DeepEqual(rows, saved) {
				t.Fatal("authorization modified the original pending receipt", err)
			}
		})
	}
}
