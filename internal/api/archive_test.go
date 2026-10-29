package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func archiveAPIEnv(t *testing.T) (*env, string, []byte) {
	t.Helper()
	e := newEnv(t)
	source := filepath.Join(t.TempDir(), "old.db")
	old, err := testkit.ArchiveLedger57(t.Context(), source, "../store/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecContext(t.Context(), `INSERT INTO deployment_identity VALUES(1,'historical-deployment')`); err != nil {
		t.Fatal(err)
	}
	files, err := blobstore.New(source + ".files")
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("\x89PNG\r\n\x1a\narchived original\x00\xff")
	object, err := files.Put(t.Context(), bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	html := []byte("<!doctype html><script>fetch('/api/v1/ops')</script><p>original report</p>")
	digest := sha256.Sum256(html)
	for _, id := range []string{"a-message", "b-message"} {
		if _, err = old.ExecContext(t.Context(), `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES(?,'owner',?,1)`, id, `Historical raw request {"old_field":true}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = old.ExecContext(t.Context(), `INSERT INTO conversation_attachments(file_id,name,media_type,size,sha256,created_at) VALUES('original-image','picture.png','image/png',?,?,1)`, len(original), object.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecContext(t.Context(), `INSERT INTO conversation_message_attachments(message_id,file_id,position) VALUES('a-message','original-image',0)`); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO tasks(task_id,title,lifecycle,control_mode,control_revision,spec_version,priority,block_reason,created_at,updated_at) VALUES('old-task','Archived task','queued','managed',1,1,0,'none',1,1)`,
		`INSERT INTO task_specs VALUES('old-task',1,'{"goal":"historical raw"}',NULL,1)`,
		`INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES('old-action','old-task','implement','old-effect','{}',1,'{}',1,'done','old-request',1,1)`,
		`INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES('old-run','old-task','old-action',1,'implement','stop','finished',1,1)`,
		`INSERT INTO agent_thread_history VALUES('uncollected-thread','main',1)`,
	}
	for _, statement := range statements {
		if _, err = old.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = old.ExecContext(t.Context(), `INSERT INTO deliverable_files(file_id,task_id,run_id,name,size,sha256,content,created_at) VALUES('original-report','old-task','old-run','report.html',?,?,?,1)`, len(html), hex.EncodeToString(digest[:]), html); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	native := t.TempDir()
	for _, name := range []string{"first.jsonl", "second.jsonl"} {
		if err = os.WriteFile(filepath.Join(native, name), []byte("{\"native\":\""+name+"\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(t.TempDir(), "sealed")
	if _, err = archive.Capture(t.Context(), archive.CaptureOptions{Database: source, Destination: root, WritersStopped: true, CapturedAt: e.clock.Now(), Resources: []archive.ResourceSource{{ID: "native-child", Kind: "native_history", ThreadID: "child", ParentThreadID: "parent", Scope: "Explicit child transcript files", Path: native, Status: "partial", Reason: "Parent not collected"}}}); err != nil {
		t.Fatal(err)
	}
	reader, err := archive.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	e.server = New(e.app, e.store, e.peer, secret, Config{Owners: []string{ownerLogin, "other-owner@example.com"}, Now: e.clock.Now, Archives: map[string]ConfiguredArchive{
		"history": {Reader: reader}, "other-history": {Reader: reader}, "offline": {State: protocol.ArchiveUnavailable, Reason: "missing"}, "future": {State: protocol.ArchiveUnsupported, Reason: "unsupported_schema"},
	}}, quiet())
	server := httptest.NewServer(e.server.Handler())
	t.Cleanup(server.Close)
	e.url = server.URL
	return e, root, original
}
func archiveTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, p)
		if entry.IsDir() {
			out[relative+"/"] = "dir"
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		out[relative] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestArchiveOwnerBrowseUsesSharedReaderAndKeepsHistoricalBytes(t *testing.T) {
	e, root, _ := archiveAPIEnv(t)
	before := archiveTree(t, root)
	owner := e.login(protocol.ScopeOwner)
	var catalog protocol.ArchiveCatalog
	if code := e.do("GET", "/api/v1/archives", owner, nil, &catalog); code != 200 || len(catalog.Items) != 4 {
		t.Fatal(code, catalog)
	}
	raw, _ := json.Marshal(catalog)
	if bytes.Contains(raw, []byte(root)) || bytes.Contains(raw, []byte("source_build")) {
		t.Fatal("catalog invented metadata or exposed local directory", string(raw))
	}
	var page protocol.ArchiveRecordsPage
	for range 3 {
		if code := e.do("GET", "/api/v1/archives/history/records/message?limit=1", owner, nil, &page); code != 200 || len(page.Items) != 1 || page.NextAfter != "a-message" || page.Schema != 57 {
			t.Fatal(code, page)
		}
	}
	if code := e.do("GET", "/api/v1/archives/history/records/message?limit=1&after="+page.NextAfter, owner, nil, &page); code != 200 || page.Items[0]["message_id"] != "b-message" {
		t.Fatal(code, page)
	}
	var record protocol.ArchiveRecord
	if code := e.do("GET", "/api/v1/archives/history/records/message/a-message", owner, nil, &record); code != 200 || record.Data["body"] != "Historical raw request {\"old_field\":true}" {
		t.Fatal(code, record)
	}
	if code := e.do("GET", "/api/v1/archives/history/records/task/old-task", owner, nil, &record); code != 200 {
		t.Fatal("historical task missing", code)
	}
	if code := e.do("GET", "/api/v1/tasks/old-task", owner, nil, nil); code != 404 {
		t.Fatal("historical task entered current ledger", code)
	}
	var originals protocol.ArchiveOriginalPage
	if code := e.do("GET", "/api/v1/archives/history/originals/attachment", owner, nil, &originals); code != 200 || len(originals.Items) != 1 || originals.Items[0].ID != "original-image" {
		t.Fatal(code, originals)
	}
	var resources protocol.ArchiveResourcePage
	if code := e.do("GET", "/api/v1/archives/history/resources", owner, nil, &resources); code != 200 {
		t.Fatal(code)
	}
	foundPartial, foundMissing := false, false
	for _, resource := range resources.Items {
		if resource.ID == "native-child" {
			foundPartial = resource.Status == "partial" && resource.ParentThreadID == "parent" && len(resource.Files) == 2
		}
		if resource.ThreadID == "uncollected-thread" {
			foundMissing = resource.Status == "not_collected" && len(resource.Files) == 0
		}
	}
	if !foundPartial || !foundMissing {
		t.Fatal(resources)
	}
	encoded, _ := json.Marshal(resources)
	if bytes.Contains(encoded, []byte(`"path"`)) || bytes.Contains(encoded, []byte(root)) {
		t.Fatal("resource API exposes local/payload paths")
	}
	for _, route := range []string{"/records/message?limit=0", "/records/no-sql", "/resources?limit=201", "/resources/native-child/files/no-index", "/originals/no-kind"} {
		if code := e.do("GET", "/api/v1/archives/history"+route, owner, nil, nil); code != 400 {
			t.Fatal(route, code)
		}
	}
	for _, route := range []string{"/api/v1/archives/unknown", "/api/v1/archives/history/records/message/missing", "/api/v1/archives/history/originals/attachment/missing/metadata", "/api/v1/archives/history/resources/native-child/files/99"} {
		if code := e.do("GET", route, owner, nil, nil); code != 404 {
			t.Fatal(route, code)
		}
	}
	if code := e.do("GET", "/api/v1/archives/offline", owner, nil, nil); code != 503 {
		t.Fatal(code)
	}
	if code := e.do("GET", "/api/v1/archives/future/records/message", owner, nil, nil); code != 503 {
		t.Fatal(code)
	}
	if code := e.do("POST", "/api/v1/archives/history", owner, map[string]string{}, nil); code != 405 {
		t.Fatal("archive gained mutation entry", code)
	}
	if !reflect.DeepEqual(before, archiveTree(t, root)) {
		t.Fatal("owner browse changed sealed archive")
	}
}

func TestArchiveOriginalAndResourceGrantsPreserveScopeAndPreviewPolicy(t *testing.T) {
	e, root, original := archiveAPIEnv(t)
	before := archiveTree(t, root)
	owner := e.login(protocol.ScopeOwner)
	get := func(route, rangeHeader string) (*http.Response, []byte) {
		t.Helper()
		request, _ := http.NewRequestWithContext(t.Context(), "GET", e.url+route, nil)
		if rangeHeader != "" {
			request.Header.Set("Range", rangeHeader)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return response, body
	}
	base := "/api/v1/archives/history/originals/attachment/original-image"
	var grant protocol.DownloadURLResponse
	if code := e.do("GET", base+"/download-url?inline=1", owner, nil, &grant); code != 200 || strings.Contains(grant.URL, owner) {
		t.Fatal(code, grant)
	}
	response, body := get(grant.URL, "")
	expectedHash := sha256.Sum256(original)
	if response.Header.Get("X-Content-SHA256") != hex.EncodeToString(expectedHash[:]) {
		t.Fatal("original content hash header lost")
	}
	if response.StatusCode != 200 || !bytes.Equal(body, original) || response.Header.Get("Content-Type") != "image/png" {
		t.Fatal(response.StatusCode, string(body), response.Header)
	}
	response, body = get(grant.URL, "bytes=1-4")
	if response.StatusCode != 206 || !bytes.Equal(body, original[1:5]) {
		t.Fatal("range lost bytes", response.StatusCode, body)
	}
	for _, changed := range []string{strings.Replace(grant.URL, "/history/", "/other-history/", 1), strings.Replace(grant.URL, "/attachment/", "/image/", 1), strings.Replace(grant.URL, "original-image", "other-id", 1), strings.Replace(grant.URL, "inline=1", "inline=0", 1)} {
		if code := e.do("GET", changed, "", nil, nil); code != 401 {
			t.Fatal("grant escaped scope", code, changed)
		}
	}
	if code := e.do("GET", strings.Replace(grant.URL, "original-image?", "original-image/metadata?", 1), "", nil, nil); code != 401 {
		t.Fatal("download grant read metadata", code)
	}
	legacyGrant := e.server.signFileGrant(fileGrant{Login: ownerLogin, ID: "original-image", Kind: "attachment", Inline: true, Issued: e.clock.Now().UnixMilli()})
	if code := e.do("GET", base+"?download="+url.QueryEscape(legacyGrant)+"&inline=1", "", nil, nil); code != 401 {
		t.Fatal("legacy file grant read archive", code)
	}
	htmlBase := "/api/v1/archives/history/originals/deliverable/original-report"
	if code := e.do("GET", htmlBase+"/download-url?inline=1", owner, nil, &grant); code != 200 {
		t.Fatal(code)
	}
	response, body = get(grant.URL, "")
	if response.StatusCode != 200 || !bytes.Contains(body, []byte("<script>")) || response.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal(response.StatusCode, string(body), response.Header)
	}
	csp := response.Header.Get("Content-Security-Policy")
	for _, required := range []string{"sandbox;", "script-src 'none'", "connect-src 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, required) {
			t.Fatal("archive active document policy", csp)
		}
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Fatal(csp)
	}
	resourceBase := "/api/v1/archives/history/resources/native-child/files/0"
	if code := e.do("GET", resourceBase+"/download-url", owner, nil, &grant); code != 200 {
		t.Fatal(code)
	}
	response, body = get(grant.URL, "")
	if response.StatusCode != 200 || !bytes.Contains(body, []byte("first.jsonl")) || !strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal(response.StatusCode, string(body), response.Header)
	}
	if code := e.do("GET", strings.Replace(grant.URL, "/files/0?", "/files/1?", 1), "", nil, nil); code != 401 {
		t.Fatal("resource grant changed index", code)
	}
	if code := e.do("GET", resourceBase+"/download-url?inline=1", owner, nil, nil); code != 400 {
		t.Fatal(code)
	}
	e.peer.set(netx.Identity{User: "other-owner@example.com"})
	if code := e.do("GET", grant.URL, "", nil, nil); code != 401 {
		t.Fatal("grant changed owner", code)
	}
	e.peer.set(netx.Identity{User: ownerLogin})
	e.clock.Advance(6 * time.Minute)
	if code := e.do("GET", grant.URL, "", nil, nil); code != 401 {
		t.Fatal("expired grant accepted", code)
	}
	if !reflect.DeepEqual(before, archiveTree(t, root)) {
		t.Fatal("downloads changed sealed archive")
	}
}

func TestArchiveOwnerAuthenticationDoesNotGrantAgentOrOldInstanceAccess(t *testing.T) {
	e, _, _ := archiveAPIEnv(t)
	owner := e.login(protocol.ScopeOwner)
	agent := e.server.sign(token{Login: ownerLogin, ClientID: clientID, Scope: protocol.ScopeAgent, Issued: e.clock.Now().UnixMilli()})
	old := New(e.app, e.store, e.peer, bytes.Repeat([]byte{9}, 32), Config{Owners: []string{ownerLogin}, Now: e.clock.Now}, quiet()).sign(token{Login: ownerLogin, ClientID: clientID, Scope: protocol.ScopeOwner, Issued: e.clock.Now().UnixMilli()})
	for _, route := range []string{"/api/v1/archives", "/api/v1/archives/history", "/api/v1/archives/history/records/message", "/api/v1/archives/history/originals/attachment/original-image/metadata", "/api/v1/archives/history/resources", "/api/v1/archives/history/resources/native-child/files/0/download-url"} {
		for _, unauthorized := range []string{"", agent, old} {
			if code := e.do("GET", route, unauthorized, nil, nil); code != 401 {
				t.Fatal(route, code)
			}
		}
	}
	e.peer.set(netx.Identity{User: "stranger@example.com"})
	if code := e.do("GET", "/api/v1/archives", owner, nil, nil); code != 403 {
		t.Fatal(code)
	}
}
