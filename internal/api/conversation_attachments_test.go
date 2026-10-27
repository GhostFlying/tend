package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type zeroFileReader struct{}

func (zeroFileReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestStreamingAttachmentsExceedOldRequestLimitAndScopedDownload(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	reqID := e.ids.New(protocol.PrefixRequest)
	query := url.Values{"name": {"large.bin"}, "media_type": {"application/octet-stream"}, "request_id": {reqID}}
	const size = int64(33 << 20)
	req, _ := http.NewRequest("POST", e.url+"/api/v1/conversation/attachments?"+query.Encode(), io.LimitReader(zeroFileReader{}, size))
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(deploymentHeader, deployment)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var reply protocol.APIResponse
	err = json.NewDecoder(response.Body).Decode(&reply)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || reply.Operation == nil || reply.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("upload: %d %+v %v", response.StatusCode, reply, err)
	}
	var meta protocol.ConversationAttachment
	if err := json.Unmarshal(reply.Operation.Result, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Size != size {
		t.Fatalf("size %d", meta.Size)
	}
	route := "/api/v1/conversation/attachments/" + meta.FileID
	if status := e.do("GET", route, "", nil, nil); status != 401 {
		t.Fatalf("unauth download: %d", status)
	}
	var grant protocol.DownloadURLResponse
	if status := e.do("GET", route+"/download-url", token, nil, &grant); status != 200 || strings.Contains(grant.URL, token) {
		t.Fatalf("grant: %d", status)
	}
	got, err := http.Get(e.url + grant.URL)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, got.Body)
	got.Body.Close()
	if err != nil || n != size || got.StatusCode != 200 || got.Header.Get("X-Content-SHA256") != meta.SHA256 || hex.EncodeToString(hash.Sum(nil)) != meta.SHA256 {
		t.Fatalf("download %d %d %v", got.StatusCode, n, err)
	}
	if !strings.HasPrefix(got.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal("download must be a file")
	}
	if status := e.do("GET", grant.URL+"&inline=1", "", nil, nil); status != 401 {
		t.Fatalf("grant mode broadened: %d", status)
	}
	if status := e.do("GET", strings.Replace(grant.URL, meta.FileID, "different", 1), "", nil, nil); status != 401 {
		t.Fatalf("grant file broadened: %d", status)
	}
	e.peer.set(netx.Identity{User: "other-owner@example.com"})
	if status := e.do("GET", grant.URL, "", nil, nil); status != 401 {
		t.Fatalf("different owner: %d", status)
	}
	e.peer.set(netx.Identity{User: ownerLogin})
	e.clock.Advance(6 * time.Minute)
	if status := e.do("GET", grant.URL, "", nil, nil); status != 401 {
		t.Fatalf("expired grant: %d", status)
	}
	var count int
	if err := e.store.Read(context.Background(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM conversation_attachments`).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("metadata: %d %v", count, err)
	}
}

func TestAttachmentTextIsAlwaysDownloadedEvenWhenPreviewRequested(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	op, err := e.app.UploadConversationAttachment(context.Background(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: ownerLogin, Client: clientID}, e.ids.New(protocol.PrefixRequest), "unsafe.html", "text/html", strings.NewReader("<script>throw 1</script>"))
	if err != nil {
		t.Fatal(err)
	}
	var f protocol.ConversationAttachment
	_ = json.Unmarshal(op.Result, &f)
	var grant protocol.DownloadURLResponse
	if status := e.do("GET", "/api/v1/conversation/attachments/"+f.FileID+"/download-url?inline=1", token, nil, &grant); status != 200 {
		t.Fatal(status)
	}
	res, err := http.Get(e.url + grant.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("active file rendered: %v", res.Header)
	}
}
