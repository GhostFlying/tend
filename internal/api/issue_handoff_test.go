package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIssueHandoffPromptHTTPCLIRequiresOwnerAndExactResponsibility(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	task := e.task("Original work")
	run := e.startRun(task)
	path := "/api/v1/issues/" + issue.IssueID + "/handoff-prompt?run=" + run
	if code := e.do("GET", path, "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if code := e.do("GET", path, token, nil, nil); code != http.StatusForbidden {
		t.Fatal("foreign Run admitted", code)
	}
	var earliest string
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE task_work_item_owners SET work_item_id=? WHERE task_id=?`, issue.IssueID, task); err != nil {
			return err
		}
		for i := 0; i < 55; i++ {
			id := e.ids.New(protocol.PrefixMessage)
			if i == 0 {
				earliest = id
			}
			if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, ConversationID: "main", Author: "owner", Body: fmt.Sprintf("Scope correction %d", i), CreatedAt: e.clock.Now().UnixMilli()}); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'owner',?)`, id, issue.IssueID, e.clock.Now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := e.count("SELECT COUNT(*) FROM events")
	var result protocol.IssueHandoffPrompt
	if code := e.do("GET", path, token, nil, &result); code != http.StatusOK {
		t.Fatal(code)
	}
	if result.RunID != run || result.TaskID != task || !result.SourcesMore || result.NextSource == 0 || result.ContextRevision == 0 || !strings.Contains(result.Prompt, "not complete history") {
		t.Fatalf("%+v", result)
	}
	var context protocol.IssueContextView
	if code := e.do("GET", "/api/v1/issues/"+issue.IssueID+"/context?after=0", token, nil, &context); code != 200 || len(context.Sources) == 0 || context.Sources[0].MessageID != earliest {
		t.Fatal("early scope unavailable", code, context)
	}
	dir := t.TempDir()
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"config.json": map[string]string{"server": e.url}, "credentials.json": map[string]string{"client_id": "handoff", "owner_token": token, "deployment_id": deployment}} {
		raw, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, ConfigDir: dir, HTTP: http.DefaultClient, IDs: e.ids}
	for _, asJSON := range []bool{false, true} {
		stdout.Reset()
		args := []string{"issue", "handoff-prompt", issue.IssueID, "--run", run}
		if asJSON {
			args = append(args, "--json")
		}
		if code := cli.Main(t.Context(), env, args); code != 0 {
			t.Fatal(code, stderr.String())
		}
		if asJSON {
			var v protocol.IssueHandoffPrompt
			if json.Unmarshal(stdout.Bytes(), &v) != nil || v.Prompt != result.Prompt {
				t.Fatal(stdout.String())
			}
		} else if stdout.String() != result.Prompt {
			t.Fatal(stdout.String())
		}
	}
	if after := e.count("SELECT COUNT(*) FROM events"); after != before {
		t.Fatal("read-only prompt wrote requests", before, after)
	}
}
