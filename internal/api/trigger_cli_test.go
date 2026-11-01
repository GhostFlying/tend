package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/webhookauth"
)

func TestTriggerCLIRealHTTPCompleteManagement(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	app.WithFollowups(true)(e.app)
	keys := &webhookauth.Files{Dir: filepath.Join(t.TempDir(), "keys")}
	e.server.cfg.WebhookKeys = keys
	app.WithWebhookKeys(keys)(e.app)
	dir := t.TempDir()
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"config.json": map[string]string{"server": e.url}, "credentials.json": map[string]string{"client_id": "trigger-cli", "owner_token": token, "deployment_id": deployment}} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, stderr bytes.Buffer
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &stderr, ConfigDir: dir, HTTP: http.DefaultClient, IDs: e.ids}
	run := func(args ...string) []byte {
		t.Helper()
		out.Reset()
		stderr.Reset()
		if code := cli.Main(t.Context(), env, args); code != 0 {
			t.Fatalf("%v exit=%d out=%s stderr=%s", args, code, out.String(), stderr.String())
		}
		return append([]byte(nil), out.Bytes()...)
	}
	spec := protocol.FollowupSpec{Purpose: "Check only the agreed result", Instruction: "Read facts", Kind: "cron", Cron: "0 9 * * *", Timezone: "UTC", ExpiresAt: e.clock.Now().Add(72 * time.Hour).UnixMilli()}
	raw, _ := json.Marshal(spec)
	var response protocol.APIResponse
	_ = json.Unmarshal(run("schedule", "create", "--issue", issue.IssueID, "--data", string(raw)), &response)
	if response.Operation == nil {
		t.Fatal(response)
	}
	var rule protocol.IssueFollowupRule
	if err = json.Unmarshal(response.Operation.Result, &rule); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pause", "resume", "update", "trigger", "cancel"} {
		args := []string{"schedule", action, rule.RuleID, "--reason", "owner checks agreed work"}
		if action == "update" {
			args = append(args, "--data", string(raw))
		}
		if err = json.Unmarshal(run(args...), &response); err != nil {
			t.Fatal(err)
		}
	}
	var history protocol.IssueFollowupHistory
	if err = json.Unmarshal(run("schedule", "history", rule.RuleID), &history); err != nil || history.Rule.Status != "cancelled" || len(history.Occurrences) != 1 {
		t.Fatal(history, err)
	}
	keyPath := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(keyPath, bytes.Repeat([]byte("s"), 32), 0600); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(run("webhook", "create", "--issue", issue.IssueID, "--secret-file", keyPath, "--data", `{"title":"External build","instruction":"Inspect facts without expanding authority"}`), &response)
	var sub protocol.WebhookSubscription
	if response.Operation == nil || json.Unmarshal(response.Operation.Result, &sub) != nil || sub.ID == "" {
		t.Fatal(response)
	}
	for _, action := range []string{"pause", "resume", "update", "rotate", "cancel"} {
		args := []string{"webhook", action, sub.ID, "--reason", "owner updates the subscription"}
		if action == "update" {
			args = append(args, "--data", `{"title":"External build result","instruction":"Inspect the current Issue"}`)
		}
		if action == "rotate" {
			args = append(args, "--secret-file", keyPath)
		}
		if err = json.Unmarshal(run(args...), &response); err != nil {
			t.Fatal(err)
		}
	}
	var list protocol.WebhookSubscriptionPage
	if json.Unmarshal(run("issue", "webhooks", issue.IssueID, "--json"), &list) != nil || len(list.Items) != 1 || list.Items[0].Status != "cancelled" {
		t.Fatal(list)
	}
	var events protocol.TriggerEventPage
	if json.Unmarshal(run("event", "list", "--issue", issue.IssueID, "--json"), &events) != nil || len(events.Items) != 1 {
		t.Fatal(events)
	}
	var event protocol.TriggerEvent
	if json.Unmarshal(run("event", "trace", events.Items[0].ID, "--json"), &event) != nil || len(event.Steps) == 0 {
		t.Fatal(event)
	}
	run("request", "show", response.Operation.RequestID, "--json")
	if strings.Contains(out.String(), strings.Repeat("s", 32)) {
		t.Fatal("private key leaked")
	}
}
