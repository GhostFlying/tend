package runnerapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBinaryFileUploadReplayAndAcceptanceDownload(t *testing.T) {
	e := newEnv(t, 10*time.Millisecond)
	e.createTask()
	as := e.claim(e.envelope(protocol.OpRunnerClaim, "", map[string]any{}))
	if as == nil {
		t.Fatal("no run")
	}
	id := e.ids.New(protocol.PrefixRequest)
	endpoint := e.server.URL + "/runner/v1/files?request_id=" + url.QueryEscape(id) + "&name=" + url.QueryEscape("调查.txt")
	content := []byte{0, 1, 2, 255, '\n'}
	upload := func(body []byte) (int, response) {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+as.RunToken)
		r.Header.Set(HeaderRun, as.RunID)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var got response
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, got
	}
	status, first := upload(content)
	if status != http.StatusOK || first.Operation == nil || first.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("upload: %d %+v", status, first)
	}
	var file protocol.ArtifactFile
	if err := json.Unmarshal(first.Operation.Result, &file); err != nil {
		t.Fatal(err)
	}
	status, repeated := upload(content)
	if status != http.StatusOK || !bytes.Equal(first.Operation.Result, repeated.Operation.Result) {
		t.Fatalf("replay: %d %+v", status, repeated)
	}
	status, conflict := upload([]byte("different"))
	if status != http.StatusConflict || conflict.Error == nil || conflict.Error.Code != protocol.CodeConflictPayload {
		t.Fatalf("changed bytes reused request ID: %d %+v", status, conflict)
	}
	get := func(c credential) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(http.MethodGet, e.server.URL+"/runner/v1/files/"+file.FileID, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+c.token)
		r.Header.Set(c.header, c.subject)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusOK && resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing download protection")
		}
		return resp.StatusCode, body
	}
	if status, _ := get(asHost()); status != http.StatusForbidden {
		t.Fatalf("host read files: %d", status)
	}
	if status, body := get(asRun(*as)); status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("producer read: %d %q", status, body)
	}
	e.mustOK(asRun(*as), e.envelope(protocol.OpRunsSubmitArtifact, as.RunID, protocol.ArtifactPayload{
		Mounts: []protocol.ArtifactMount{{Repo: "tend", Path: "tend", BaseSHA: baseSHA, CommitSHA: commitSHA, Changed: true}},
		Report: &protocol.ResultReport{Body: "Findings attached"}, Files: []protocol.ArtifactFile{file},
	}))
	accept := e.claim(e.envelope(protocol.OpRunnerClaim, "", map[string]any{}))
	if accept == nil || accept.Role != protocol.RoleAccept {
		t.Fatalf("no acceptance: %+v", accept)
	}
	if status, body := get(asRun(*accept)); status != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("acceptance download: %d %q", status, body)
	}
}
