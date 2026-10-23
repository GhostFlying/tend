package api

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOwnerImagesUploadReadAndReplay(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.HostedMainAgent = true
	token := e.login(protocol.ScopeOwner)
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	// A valid PNG with trailing bytes exercises uploads beyond the old 1MiB HTTP limit.
	raw := append(pngBytes.Bytes(), make([]byte, 1<<20)...)
	encoded := base64.StdEncoding.EncodeToString(raw)
	env := e.envelope("conversation.submit_message", protocol.Target{Type: "conversation"}, nil, protocol.SubmitMessagePayload{Images: []protocol.ConversationImageInput{{Name: "picture.png", MediaType: "image/png", DataBase64: encoded}}})
	for range 2 {
		st, res := e.op(token, env)
		if st != http.StatusOK || res.Operation == nil || res.Operation.Status == protocol.OpFailed {
			t.Fatalf("upload=%d %+v", st, res)
		}
	}
	st, page := e.conversation(token, "")
	if st != http.StatusOK || len(page.Items) != 1 || len(page.Items[0].Images) != 1 || page.Items[0].Body != "" {
		t.Fatalf("conversation=%d %+v", st, page)
	}
	img := page.Items[0].Images[0]
	if img.Name != "picture.png" || img.Size != int64(len(raw)) {
		t.Fatalf("metadata=%+v", img)
	}
	var result protocol.ConversationImageResponse
	path := "/api/v1/conversation/images/" + img.ImageID
	if st := e.do("GET", path, token, nil, &result); st != http.StatusOK || result.Image.DataBase64 != encoded {
		t.Fatalf("download=%d, bytes equal=%v", st, result.Image.DataBase64 == encoded)
	}
	if st := e.do("GET", path, "", nil, nil); st != http.StatusUnauthorized {
		t.Fatalf("unauthenticated image=%d", st)
	}
	if st := e.do("GET", "/api/v1/conversation/images/missing", token, nil, nil); st != http.StatusNotFound {
		t.Fatalf("missing=%d", st)
	}
}

func TestNonUploadOperationsKeepSmallPayloadLimit(t *testing.T) {
	e := newEnv(t)
	e.server.cfg.HostedMainAgent = true
	token := e.login(protocol.ScopeOwner)
	env := e.envelope("conversation.interrupt_reply", protocol.Target{Type: "message"}, nil, protocol.InterruptReplyPayload{MessageID: strings.Repeat("x", maxBody+1)})
	st, res := e.op(token, env)
	if st != http.StatusBadRequest || res.Error == nil || res.Error.Code != protocol.CodeInvalidRequest || !strings.Contains(res.Error.Message, "too large") {
		t.Fatalf("oversized nonupload=%d %+v", st, res)
	}
}
