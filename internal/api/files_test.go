package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDeliverablePreviewPreservesBytesAndScopesGrants(t *testing.T) {
	e := newEnv(t)
	e.configure()
	task := e.task("preview files")
	run := e.startRun(task)
	token := e.login(protocol.ScopeOwner)
	for _, tc := range []struct{ name, body, media, disposition string }{
		{"picture.png", "\x89PNG\r\n\x1a\noriginal bytes", "image/png", "inline"},
		{"fake.png", "<script>parent.location='https://example.com'</script>", "application/octet-stream", "attachment"},
		{"diagram.svg", "<svg onload='alert(1)'/>", "application/octet-stream", "attachment"},
		{"design.html", "<!doctype html><script>fetch('/api/v1/ops')</script>", "text/html; charset=utf-8", "inline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op, err := e.app.UploadDeliverableFile(context.Background(), protocol.Principal{Kind: protocol.PrincipalRun, Subject: run, Role: protocol.RoleImplement}, e.ids.New(protocol.PrefixRequest), tc.name, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			var file protocol.ArtifactFile
			if err := json.Unmarshal(op.Result, &file); err != nil || file.FileID == "" {
				t.Fatalf("upload: %s %v", op.Result, err)
			}
			route := "/api/v1/files/" + file.FileID
			var meta protocol.ArtifactFile
			if status := e.do("GET", route+"/metadata", token, nil, &meta); status != 200 || meta != file {
				t.Fatalf("metadata: %d %+v", status, meta)
			}
			if status := e.do("GET", route+"/metadata", "", nil, nil); status != 401 {
				t.Fatalf("metadata without owner: %d", status)
			}
			var grant protocol.DownloadURLResponse
			if status := e.do("GET", route+"/download-url?inline=1", token, nil, &grant); status != 200 || strings.Contains(grant.URL, token) {
				t.Fatalf("grant: %d", status)
			}
			res, err := http.Get(e.url + grant.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != 200 || !bytes.Equal(body, []byte(tc.body)) {
				t.Fatalf("preview bytes: %d %v", res.StatusCode, err)
			}
			if res.Header.Get("Content-Type") != tc.media || !strings.HasPrefix(res.Header.Get("Content-Disposition"), tc.disposition) {
				t.Fatalf("preview headers: %v", res.Header)
			}
			csp := res.Header.Get("Content-Security-Policy")
			if !strings.Contains(csp, "sandbox;") || strings.Contains(csp, "allow-same-origin") || !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'none'") || !strings.Contains(csp, "form-action 'none'") {
				t.Fatalf("unsafe preview CSP: %q", csp)
			}
			if status := e.do("GET", strings.Replace(grant.URL, "inline=1", "inline=0", 1), "", nil, nil); status != 401 {
				t.Fatalf("changed presentation: %d", status)
			}
			if status := e.do("GET", strings.Replace(grant.URL, file.FileID, "file_other", 1), "", nil, nil); status != 401 {
				t.Fatalf("changed file: %d", status)
			}
			e.peer.set(netx.Identity{User: "other-owner@example.com"})
			if status := e.do("GET", grant.URL, "", nil, nil); status != 401 {
				t.Fatalf("changed owner: %d", status)
			}
			e.peer.set(netx.Identity{User: ownerLogin})
			e.clock.Advance(6 * time.Minute)
			if status := e.do("GET", grant.URL, "", nil, nil); status != 401 {
				t.Fatalf("expired grant: %d", status)
			}
			if status := e.do("GET", route+"/download-url", token, nil, &grant); status != 200 {
				t.Fatal(status)
			}
			res, err = http.Get(e.url + grant.URL)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
				t.Fatalf("ordinary download changed: %v", res.Header)
			}
		})
	}
}
