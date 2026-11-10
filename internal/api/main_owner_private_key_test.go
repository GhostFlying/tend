package api

import (
	"bytes"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/webhookauth"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainOwnerStagesPrivateWebhookKeyThroughOpaqueReference(t *testing.T) {
	e, call := mainOwnerFixture(t)
	keys := &webhookauth.Files{Dir: filepath.Join(t.TempDir(), "private-keys")}
	e.server.cfg.WebhookKeys = keys
	source, destination := e.ids.New(protocol.PrefixRequest), e.ids.New(protocol.PrefixRequest)
	secret := bytes.Repeat([]byte("private-signing-material"), 2)
	if err := keys.Put(t.Context(), source, secret); err != nil {
		t.Fatal(err)
	}
	call.Name = ownerToolName("PUT /api/v1/webhook-keys/{id}")
	call.RequestID = e.ids.New(protocol.PrefixRequest)
	call.Arguments, _ = json.Marshal(protocol.OwnerToolArguments{EffectSeq: 1, Path: map[string]string{"id": destination}, WebhookKeyRef: source})
	result, err := e.server.MainOwnerTools().Call(t.Context(), call)
	if err != nil {
		t.Fatal(result, err)
	}
	saved, err := keys.Get(t.Context(), destination)
	if err != nil || !bytes.Equal(saved, secret) {
		t.Fatal("private reference did not stage identical bytes", err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), string(secret)) {
		t.Fatal("private bytes entered tool result")
	}
	raw, _ := json.Marshal(map[string]any{"name": call.Name, "arguments": map[string]any{"body": string(secret), "content_base64": "secret-base64", "webhook_key_ref": source}})
	redacted := protocol.RedactOwnerToolInput("owner_call", string(raw))
	if strings.Contains(redacted, string(secret)) || strings.Contains(redacted, "secret-base64") || !strings.Contains(redacted, source) {
		t.Fatal("credential observation not redacted")
	}
	call.RequestID = e.ids.New(protocol.PrefixRequest)
	call.Arguments, _ = json.Marshal(protocol.OwnerToolArguments{EffectSeq: 2, Path: map[string]string{"id": destination}, Body: json.RawMessage(`"private-bytes"`)})
	if _, err := e.server.MainOwnerTools().Call(t.Context(), call); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("raw key input accepted", err)
	}
}
