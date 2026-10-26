package runnerapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestObservationEndpointAuthenticatesRunAndHost(t *testing.T) {
	e := newEnv(t, 10*time.Millisecond)
	payload, _ := json.Marshal(map[string]any{"title": "observe", "spec": domain.Spec{Goal: "verify current deployment", Acceptance: []string{"actual state"}, Delivery: domain.DeliveryCommit, Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}, Observation: &protocol.ObservationScope{Deployment: true}}})
	env := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "tasks.create", Target: protocol.Target{Type: "task"}, Payload: payload}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}
	if op, err := e.app.Execute(t.Context(), owner, env, app.CreateTask{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("create %v %+v", err, op)
	}
	rt := &protocol.RunnerRuntime{InstanceID: "observation", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityWorkerObservation}}
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "", protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: rt}))
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "", protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: rt}))
	if as == nil {
		t.Fatal("no claim")
	}
	e.mustOK(asRun(*as), e.envelope(protocol.OpRunsReportStarted, as.RunID, nil))
	path := "/runner/v1/observation"
	request := func(token, run, host string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(HeaderRun, run)
		req.Header.Set(HeaderHost, host)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("cacheable snapshot")
		}
		if res.StatusCode == http.StatusOK {
			var v protocol.RunObservation
			if err := json.NewDecoder(res.Body).Decode(&v); err != nil || v.RunID != as.RunID || v.Deployment == nil {
				t.Fatalf("view %+v %v", v, err)
			}
		}
		return res.StatusCode
	}
	if got := request(as.RunToken, as.RunID, ""); got != http.StatusOK {
		t.Fatal(got)
	}
	if got := request("wrong", as.RunID, ""); got != http.StatusForbidden {
		t.Fatal(got)
	}
	if got := request(hostToken, "", "linux-1"); got != http.StatusForbidden {
		t.Fatal(got)
	}
	path += "?work_item_ids=unrelated&deployment=true"
	if got := request(as.RunToken, as.RunID, ""); got != http.StatusBadRequest {
		t.Fatal(got)
	}
	path = "/runner/v1/observation"
	e.peers.set(nil)
	if got := request(as.RunToken, as.RunID, ""); got != http.StatusForbidden {
		t.Fatal(got)
	}
}
