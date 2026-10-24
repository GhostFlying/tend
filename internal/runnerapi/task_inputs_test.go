package runnerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOriginalTaskInputDownloadsWithOnlyRunCredentials(t *testing.T) {
	e := newEnv(t, 10*time.Millisecond)
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}
	raw := []byte("GIF89a\x00first frame\x00second frame\xff")
	upload := func(name string) protocol.ConversationAttachment {
		t.Helper()
		op, err := e.app.UploadConversationAttachment(context.Background(), owner, e.ids.New(protocol.PrefixRequest), name, "image/gif", bytes.NewReader(raw))
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatalf("upload: %+v %v", op, err)
		}
		var f protocol.ConversationAttachment
		if err := json.Unmarshal(op.Result, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	file, unrelated := upload("original.gif"), upload("unrelated.gif")
	payload, _ := json.Marshal(map[string]any{"title": "inspect original", "spec": domain.Spec{
		Goal: "inspect original frames", Acceptance: []string{"original verified"}, Delivery: domain.DeliveryCommit,
		Workspace: domain.Workspace{Mounts: []domain.Mount{{Repo: "tend"}}}, Inputs: []protocol.TaskInput{{FileID: file.FileID}},
	}})
	env := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "tasks.create", Target: protocol.Target{Type: "task"}, Payload: payload}
	if op, err := e.app.Execute(context.Background(), owner, env, app.CreateTask{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("create: %+v %v", op, err)
	}
	runtime := &protocol.RunnerRuntime{InstanceID: "capable-runner", Generation: 1, Sequence: 1, Protocol: protocol.CurrentRunnerProtocol, ProtocolMin: protocol.PreviousRunnerProtocol, ProtocolMax: protocol.CurrentRunnerProtocol, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityTaskInputs}}
	e.mustOK(asHost(), e.envelope(protocol.OpRunnerHello, "", protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: runtime}))
	claim := func() *protocol.Assignment {
		return e.claim(e.envelope(protocol.OpRunnerClaim, "", protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}))
	}
	impl := claim()
	if impl == nil {
		t.Fatal("no implementation")
	}
	download := func(as protocol.Assignment, id string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, e.server.URL+"/runner/v1/files/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+as.RunToken)
		req.Header.Set(HeaderRun, as.RunID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	for _, id := range []string{file.FileID, unrelated.FileID} {
		status, got := download(*impl, id)
		if id == file.FileID {
			if status != http.StatusOK || !bytes.Equal(got, raw) {
				t.Fatalf("implementation input: %d %q", status, got)
			}
		} else if status == http.StatusOK {
			t.Fatal("unbound original downloaded")
		}
	}
	e.mustOK(asRun(*impl), e.envelope(protocol.OpRunsSubmitArtifact, impl.RunID, protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{{Repo: "tend", Path: "tend", BaseSHA: baseSHA, CommitSHA: commitSHA, Changed: true}}}))
	accept := claim()
	if accept == nil || accept.Role != protocol.RoleAccept {
		t.Fatalf("no acceptance: %+v", accept)
	}
	if status, got := download(*accept, file.FileID); status != http.StatusOK || !bytes.Equal(got, raw) {
		t.Fatalf("acceptance input: %d %q", status, got)
	}
	if status, _ := download(*accept, unrelated.FileID); status == http.StatusOK {
		t.Fatal("acceptance downloaded unbound input")
	}
}
