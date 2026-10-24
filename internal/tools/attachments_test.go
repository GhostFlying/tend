package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAttachmentsReadOriginalAndBoundedTextAfterThreadChange(t *testing.T) {
	e := newEnv(t)
	text := strings.Repeat("original text\n", 10000)
	op, err := e.svc.app.UploadConversationAttachment(context.Background(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}, "req_01M5N2YHHPQ3CFDNK56X5AGYCN", "notes.txt", "text/plain", strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	var file protocol.ConversationAttachment
	_ = json.Unmarshal(op.Result, &file)
	reply := e.call("attachments_read", fileArgs{FileID: file.FileID, MaxBytes: new(64)}, "")
	if !reply.OK {
		t.Fatalf("read: %+v", reply)
	}
	var window attachmentWindow
	if err := json.Unmarshal(reply.Result, &window); err != nil {
		t.Fatal(err)
	}
	if window.EOF || window.NextOffset != 64 || window.Text != text[:64] || window.LocalPath == "" {
		t.Fatalf("window: %+v", window)
	}
	original, err := os.ReadFile(window.LocalPath)
	if err != nil || string(original) != text {
		t.Fatal("missing original")
	}
	// A reference remains usable without the source turn's context or source message.
	raw, _ := json.Marshal(fileArgs{FileID: file.FileID, Offset: 64, MaxBytes: new(64)})
	result := e.svc.Handle(context.Background(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "harness-1", Thread: "fresh-thread"}, Native: "fresh-thread/turn/call", Tool: "attachments_read", Arguments: raw})
	if !result.Success {
		t.Fatal(result.Text)
	}
	// An owner's addon may run on another device and needs a usable download command.
	result = e.svc.Handle(context.Background(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "remote-client", Agent: protocol.AgentCodex, Thread: "remote-thread"}, Tool: "attachments_read", Arguments: raw})
	if !result.Success {
		t.Fatal(result.Text)
	}
	var addon struct {
		Result attachmentWindow `json:"result"`
	}
	if err := json.Unmarshal([]byte(result.Text), &addon); err != nil {
		t.Fatal(err)
	}
	if addon.Result.LocalPath != "" || addon.Result.DownloadCommand != "tend attachment download "+file.FileID+" --out <local-path>" || addon.Result.Text != text[64:128] {
		t.Fatalf("addon window: %+v", addon.Result)
	}
}
