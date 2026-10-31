package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestMessagingCLIAndOwnerAPI(t *testing.T) {
	e := newEnv(t)
	e.configure()
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Home: t.TempDir(), ConfigDir: filepath.Join(t.TempDir(), "config"), IDs: e.ids, Stdin: bytes.NewReader(nil), Stdout: &stdout, Stderr: &stderr, HTTP: http.DefaultClient, Getenv: func(string) string { return "" }}
	command := func(args ...string) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := cli.Main(t.Context(), env, args); code != 0 {
			t.Fatalf("%v: %d %s %s", args, code, stdout.String(), stderr.String())
		}
	}
	command("login", "--server", e.url)
	command("channel", "list")
	var channels []protocol.MessagingChannel
	if err := json.Unmarshal(stdout.Bytes(), &channels); err != nil || len(channels) != 2 {
		t.Fatal(stdout.String(), err)
	}
	command("message", "send", "--data", `{"body":"hello from IM"}`)
	var response protocol.APIResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil || response.Operation == nil {
		t.Fatal(stdout.String(), err)
	}
	var sent struct {
		ID string `json:"message_id"`
	}
	_ = json.Unmarshal(response.Operation.Result, &sent)
	command("message", "show", sent.ID)
	var message protocol.MessagingMessage
	if err := json.Unmarshal(stdout.Bytes(), &message); err != nil || message.Body != "hello from IM" {
		t.Fatal(message, err)
	}
	command("message", "receipts", sent.ID)
	var receipts protocol.MessagingReceipts
	_ = json.Unmarshal(stdout.Bytes(), &receipts)
	if len(receipts.Reads) != 0 {
		t.Fatal("query marked message read")
	}
	command("message", "read", sent.ID)
	command("message", "receipts", sent.ID)
	_ = json.Unmarshal(stdout.Bytes(), &receipts)
	if len(receipts.Reads) != 1 || receipts.Reads[0].Endpoint != "web" {
		t.Fatal(receipts)
	}
	command("message", "reply", sent.ID, "--data", `{"body":"a clarification"}`)
	command("message", "list")
	var page protocol.MessagingPage
	_ = json.Unmarshal(stdout.Bytes(), &page)
	if len(page.Items) != 2 || page.Items[1].ReplyTo != sent.ID {
		t.Fatal(page)
	}
	command("message", "trace", sent.ID)
	var trace protocol.MessagingTrace
	_ = json.Unmarshal(stdout.Bytes(), &trace)
	if len(trace.Replies) != 1 {
		t.Fatal(trace)
	}
	command("channel", "telegram", "bind", "--user-id", "123")
	command("channel", "show", "telegram")
	var channel protocol.MessagingChannel
	_ = json.Unmarshal(stdout.Bytes(), &channel)
	if channel.Status != "waiting_start" || channel.BotMarkRead {
		t.Fatal(channel)
	}
	command("channel", "telegram", "unbind")
}
