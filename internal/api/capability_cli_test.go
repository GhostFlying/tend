package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type capabilityCLI struct {
	t           *testing.T
	env         *cli.Env
	out, stderr bytes.Buffer
}

func newCapabilityCLI(t *testing.T, e *env) *capabilityCLI {
	t.Helper()
	c := &capabilityCLI{t: t}
	c.env = &cli.Env{Home: t.TempDir(), ConfigDir: t.TempDir(), IDs: e.ids, HTTP: http.DefaultClient, Stdin: strings.NewReader(""), Stdout: &c.out, Stderr: &c.stderr, Getenv: func(string) string { return "" }}
	c.run("login", "--server", e.url)
	return c
}
func (c *capabilityCLI) call(args ...string) (int, []byte) {
	c.t.Helper()
	c.out.Reset()
	c.stderr.Reset()
	code := cli.Main(c.t.Context(), c.env, args)
	return code, append([]byte(nil), c.out.Bytes()...)
}
func (c *capabilityCLI) run(args ...string) []byte {
	c.t.Helper()
	code, out := c.call(args...)
	if code != 0 {
		c.t.Fatalf("%v: exit=%d stdout=%s stderr=%s", args, code, out, &c.stderr)
	}
	return out
}

func TestCapabilityCLIRealOwnerWorkflow(t *testing.T) {
	e := newEnv(t)
	e.configure()
	owner := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, owner, "")
	c := newCapabilityCLI(t, e)
	c.run("issue", "set-status", issue.IssueID, "--data", `{"status":"in_progress"}`)
	var current protocol.IssueView
	if err := json.Unmarshal(c.run("issue", "show", issue.IssueID, "--json"), &current); err != nil || current.WorkStatus != protocol.IssueInProgress {
		t.Fatal(current, err)
	}
	var page protocol.IssuePage
	if err := json.Unmarshal(c.run("issue", "list", "--board", "--board-status", "in_progress", "--json"), &page); err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	original := c.run("issue", "notifications", "set", "--data", `{"delay_minutes":5}`, "--dry-run")
	var envelope protocol.Envelope
	if err := json.Unmarshal(original, &envelope); err != nil || envelope.ExpectedControlRevision == nil {
		t.Fatal(string(original), err)
	}
	c.env.Stdin = bytes.NewReader(original)
	c.run("request", "replay", "--file", "-")
	var settings protocol.IssueNotificationSettings
	if err := json.Unmarshal(c.run("issue", "notifications", "show", "--json"), &settings); err != nil || settings.DelayMinutes != 5 {
		t.Fatal(settings, err)
	}
	c.run("request", "replay", "--data", string(original))
	envelope.Payload = json.RawMessage(`{"delay_minutes":7}`)
	changed, _ := json.Marshal(envelope)
	if code, _ := c.call("request", "replay", "--data", string(changed)); code == 0 || !strings.Contains(c.stderr.String(), "conflict_payload") {
		t.Fatalf("changed original request was accepted: %d %s", code, &c.stderr)
	}
	var created protocol.APIResponse
	if err := json.Unmarshal(c.run("attention", "create", "--data", `{"kind":"action","title":"Check original report"}`), &created); err != nil {
		t.Fatal(err)
	}
	var attention protocol.Attention
	if err := json.Unmarshal(created.Operation.Result, &attention); err != nil {
		t.Fatal(err)
	}
	var messages protocol.MessagingPage
	if err := json.Unmarshal(c.run("message", "list"), &messages); err != nil {
		t.Fatal(err)
	}
	var question *protocol.MessagingInteraction
	for _, m := range messages.Items {
		if m.Interaction != nil && m.Interaction.ObjectID == attention.AttentionID {
			question = m.Interaction
		}
	}
	if question == nil {
		t.Fatal("action did not publish an interaction")
	}
	payload := fmt.Sprintf(`{"version":%d,"confirm":true,"text":"Checked the original report"}`, question.Version)
	c.run("message", "respond", question.ID, "--data", payload)
	var answered protocol.AttentionView
	if err := json.Unmarshal(c.run("attention", "show", attention.AttentionID), &answered); err != nil || answered.Status != "resolved" {
		t.Fatal(answered, err)
	}
	stale := fmt.Sprintf(`{"version":%d,"confirm":true}`, question.Version+10)
	if code, _ := c.call("message", "respond", question.ID, "--data", stale); code == 0 || !strings.Contains(c.stderr.String(), "stale_revision") {
		t.Fatal(code, &c.stderr)
	}
	e.server.cfg.HostedMainAgent = true
	var sent protocol.APIResponse
	if err := json.Unmarshal(c.run("conversation", "send", "main", "--data", `{"body":"A queued request"}`), &sent); err != nil {
		t.Fatal(err)
	}
	var queue protocol.IssueConversationQueuePage
	if err := json.Unmarshal(c.run("conversation", "queue", "--conversation", "main", "--json"), &queue); err != nil || len(queue.Items) == 0 {
		t.Fatal(queue, err)
	}
	message := queue.Items[len(queue.Items)-1].Item.MessageID
	c.run("message", "withdraw", message)
	var withdrawn protocol.IssueConversationQueuePage
	if err := json.Unmarshal(c.run("conversation", "queue", "--conversation", "main", "--category", "withdrawn", "--json"), &withdrawn); err != nil || len(withdrawn.Items) != 1 || withdrawn.Items[0].Item.MessageID != message {
		t.Fatal(withdrawn, err)
	}
}

func TestCapabilityCLIOriginalFilesArtifactsAndPermissions(t *testing.T) {
	e := newEnv(t)
	e.configure()
	c := newCapabilityCLI(t, e)
	original := []byte("immutable original\x00\xff\n")
	input := filepath.Join(t.TempDir(), "original.bin")
	if err := os.WriteFile(input, original, 0600); err != nil {
		t.Fatal(err)
	}
	var uploaded protocol.Operation
	if err := json.Unmarshal(c.run("attachment", "upload", input), &uploaded); err != nil {
		t.Fatal(err)
	}
	var attachment protocol.ConversationAttachment
	if err := json.Unmarshal(uploaded.Result, &attachment); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "download.bin")
	c.run("attachment", "download", attachment.FileID, "--out", output)
	if raw, err := os.ReadFile(output); err != nil || !bytes.Equal(raw, original) {
		t.Fatal(raw, err)
	}
	task := e.task("Delivery detail")
	run := e.startRun(task)
	op, err := e.app.UploadDeliverableFile(t.Context(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: run, Role: protocol.RoleImplement}, e.ids.New(protocol.PrefixRequest), "delivery.bin", bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	var file protocol.ArtifactFile
	if err = json.Unmarshal(op.Result, &file); err != nil {
		t.Fatal(err)
	}
	c.run("file", "download", file.FileID, "--out", output)
	if raw, err := os.ReadFile(output); err != nil || !bytes.Equal(raw, original) {
		t.Fatal(raw, err)
	}
	artifact := e.ids.New(protocol.PrefixArtifact)
	reference := fmt.Sprintf(`{"files":[{"file_id":%q,"name":"delivery.bin"}]}`, file.FileID)
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertArtifact(t.Context(), artifact, task, run, "files", reference, "", e.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	var detail protocol.ArtifactDetail
	if err = json.Unmarshal(c.run("artifact", "show", artifact, "--json"), &detail); err != nil || detail.TaskID != task || detail.Artifact.ArtifactID != artifact {
		t.Fatal(detail, err)
	}
	for _, token := range []string{"", e.retiredAgentToken(clientID)} {
		if status := e.do("GET", "/api/v1/artifacts/"+artifact, token, nil, nil); status != 401 && status != 403 {
			t.Fatalf("artifact allowed non-owner: %d", status)
		}
	}
}

func TestCapabilityCLIArchivePagesAndOriginalBytes(t *testing.T) {
	e, root, original := archiveAPIEnv(t)
	before := archiveTree(t, root)
	c := newCapabilityCLI(t, e)
	var page protocol.ArchiveRecordsPage
	if err := json.Unmarshal(c.run("archive", "records", "history", "message", "--limit", "1", "--json"), &page); err != nil || len(page.Items) != 1 || page.NextAfter == "" {
		t.Fatal(page, err)
	}
	var next protocol.ArchiveRecordsPage
	if err := json.Unmarshal(c.run("archive", "records", "history", "message", "--limit", "1", "--after", page.NextAfter, "--json"), &next); err != nil || len(next.Items) != 1 || reflect.DeepEqual(page.Items, next.Items) {
		t.Fatal(next, err)
	}
	output := filepath.Join(t.TempDir(), "archived.bin")
	c.run("archive", "download", "history", "attachment", "original-image", "--out", output)
	if raw, err := os.ReadFile(output); err != nil || !bytes.Equal(raw, original) {
		t.Fatal(raw, err)
	}
	var resources protocol.ArchiveResourcePage
	if err := json.Unmarshal(c.run("archive", "resources", "history", "--json"), &resources); err != nil || len(resources.Items) == 0 {
		t.Fatal(resources, err)
	}
	resource := protocol.ArchiveResource{}
	for _, item := range resources.Items {
		if item.ID == "native-child" {
			resource = item
		}
	}
	if len(resource.Files) == 0 {
		t.Fatal("original native resource is missing", resources)
	}
	c.run("archive", "resource-download", "history", resource.ID, strconv.Itoa(resource.Files[0].Index), "--out", output)
	if code, _ := c.call("archive", "show", "offline"); code == 0 {
		t.Fatal("unavailable archive reported success")
	}
	if after := archiveTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("CLI modified the historical archive")
	}
}

func TestCapabilityCLITaskFiltersAndPaginationUseActualLedger(t *testing.T) {
	e := newEnv(t)
	e.configure()
	c := newCapabilityCLI(t, e)
	type fact struct {
		project string
		state   protocol.TaskLifecycle
	}
	facts := map[string]fact{}
	for _, project := range []string{"core", "other"} {
		for _, state := range []protocol.TaskLifecycle{protocol.TaskQueued, protocol.TaskRunning, protocol.TaskCompleted, protocol.TaskCancelled} {
			id := e.task(project + " " + string(state))
			if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), "UPDATE tasks SET project=?,lifecycle=? WHERE task_id=?", project, state, id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			facts[id] = fact{project, state}
		}
	}
	cases := []struct {
		name  string
		flags []string
		want  func(protocol.TaskLifecycle) bool
	}{
		{"open", nil, func(s protocol.TaskLifecycle) bool { return !s.Terminal() }},
		{"finished", []string{"--finished"}, func(s protocol.TaskLifecycle) bool { return s.Terminal() }},
		{"all", []string{"--all"}, func(protocol.TaskLifecycle) bool { return true }},
		{"explicit lifecycle", []string{"--lifecycle", "completed"}, func(s protocol.TaskLifecycle) bool { return s == protocol.TaskCompleted }},
		{"completion ordering", []string{"--lifecycle", "completed", "--sort", "completed_at"}, func(s protocol.TaskLifecycle) bool { return s == protocol.TaskCompleted }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]bool{}
			cursor := ""
			for round := 0; round < 10; round++ {
				args := append([]string{"task", "list", "--project", "core", "--limit", "1", "--json"}, tc.flags...)
				if cursor != "" {
					args = append(args, "--before", cursor)
				}
				var page protocol.TaskPage
				if err := json.Unmarshal(c.run(args...), &page); err != nil {
					t.Fatal(err)
				}
				if len(page.Tasks) > 1 {
					t.Fatal("page size ignored", page)
				}
				for _, task := range page.Tasks {
					f, ok := facts[task.TaskID]
					if !ok || f.project != "core" || !tc.want(f.state) || seen[task.TaskID] {
						t.Fatal("filter/cursor ignored", task)
					}
					seen[task.TaskID] = true
				}
				if page.Next == "" {
					break
				}
				if page.Next == cursor {
					t.Fatal("pagination did not advance")
				}
				cursor = page.Next
				if round == 9 {
					t.Fatal("pagination did not end")
				}
			}
			want := 0
			for _, f := range facts {
				if f.project == "core" && tc.want(f.state) {
					want++
				}
			}
			if len(seen) != want {
				t.Fatalf("read %d matching tasks, want %d", len(seen), want)
			}
		})
	}
}
