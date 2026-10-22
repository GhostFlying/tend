package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = `<!doctype html><title>tend</title><div id="root"></div>`

func built() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte(indexHTML)},
		"assets/index-a1b2c3.js":  {Data: []byte("console.log(1)")},
		"assets/index-d4e5f6.css": {Data: []byte("body{}")},
		"manifest.webmanifest":    {Data: []byte(`{"name":"tend"}`)},
		"sw.js":                   {Data: []byte("self.addEventListener('fetch',()=>{})")},
		"icons/icon.svg":          {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")},
	}
}

func get(t *testing.T, h http.Handler, method, target string, header ...string) *http.Response {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result()
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestServesAssets(t *testing.T) {
	h := New(built())
	for _, tc := range []struct {
		path, contentType, cache, body string
	}{
		{"/", "text/html; charset=utf-8", "no-cache", indexHTML},
		{"/index.html", "text/html; charset=utf-8", "no-cache", indexHTML},
		{"/assets/index-a1b2c3.js", "text/javascript; charset=utf-8", immutable, "console.log(1)"},
		{"/assets/index-d4e5f6.css", "text/css; charset=utf-8", immutable, "body{}"},
		{"/manifest.webmanifest", "application/manifest+json", "no-cache", `{"name":"tend"}`},
		{"/sw.js", "text/javascript; charset=utf-8", "no-cache", "self.addEventListener('fetch',()=>{})"},
		{"/icons/icon.svg", "image/svg+xml", "no-cache", "<svg xmlns='http://www.w3.org/2000/svg'/>"},
	} {
		res := get(t, h, "GET", tc.path)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", tc.path, res.StatusCode)
			continue
		}
		if got := res.Header.Get("Content-Type"); got != tc.contentType {
			t.Errorf("GET %s: Content-Type = %q, want %q", tc.path, got, tc.contentType)
		}
		if got := res.Header.Get("Cache-Control"); got != tc.cache {
			t.Errorf("GET %s: Cache-Control = %q, want %q", tc.path, got, tc.cache)
		}
		if got := res.Header.Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Errorf("GET %s: Content-Security-Policy = %q", tc.path, got)
		}
		if got := body(t, res); got != tc.body {
			t.Errorf("GET %s = %q, want %q", tc.path, got, tc.body)
		}
	}
}

func TestRevalidatesWithETag(t *testing.T) {
	h := New(built())
	etag := get(t, h, "GET", "/").Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if res := get(t, h, "GET", "/", "If-None-Match", etag); res.StatusCode != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", res.StatusCode)
	}
	if other := get(t, h, "GET", "/sw.js").Header.Get("ETag"); other == etag {
		t.Errorf("different files share the ETag %s", etag)
	}
}

func TestAppRoutesGetIndex(t *testing.T) {
	h := New(built())
	for _, p := range []string{"/tasks", "/tasks/task_01ABC", "/assets/", "/decisions/"} {
		res := get(t, h, "GET", p)
		if res.StatusCode != http.StatusOK || body(t, res) != indexHTML {
			t.Errorf("GET %s = %d, want index.html", p, res.StatusCode)
		}
		if got := res.Header.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q", p, got)
		}
	}
}

func TestMissingFilesAreNotFound(t *testing.T) {
	h := New(built())
	for _, p := range []string{"/assets/gone-123.js", "/favicon.ico", "/icons/missing.png"} {
		if res := get(t, h, "GET", p); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, res.StatusCode)
		}
	}
}

func TestReservedPathsAreNotFound(t *testing.T) {
	h := New(built())
	for _, p := range []string{"/api", "/api/", "/api/v9/unknown", "/runner/v1/other"} {
		res := get(t, h, "GET", p)
		if res.StatusCode != http.StatusNotFound || strings.Contains(body(t, res), indexHTML) {
			t.Errorf("GET %s = %d, want a plain 404", p, res.StatusCode)
		}
	}
}

func TestOnlyReads(t *testing.T) {
	h := New(built())
	if res := get(t, h, "POST", "/"); res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST / = %d, Allow %q", res.StatusCode, res.Header.Get("Allow"))
	}
	if res := get(t, h, "HEAD", "/"); res.StatusCode != http.StatusOK || body(t, res) != "" {
		t.Errorf("HEAD / = %d", res.StatusCode)
	}
}

func TestNotBuiltPage(t *testing.T) {
	h := New(fstest.MapFS{})
	for _, p := range []string{"/", "/tasks", "/assets/index-a1b2c3.js"} {
		res := get(t, h, "GET", p)
		b := body(t, res)
		if res.StatusCode != http.StatusOK || !strings.Contains(b, "make web") {
			t.Errorf("GET %s = %d %q, want the not-built page", p, res.StatusCode, b)
		}
		if got := res.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q", p, got)
		}
	}
	if res := get(t, h, "GET", "/api/v1/session"); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/v1/session = %d, want 404", res.StatusCode)
	}
}

// TestEmbeddedBuildServes checks the embedded handler, built or not.
func TestEmbeddedBuildServes(t *testing.T) {
	res := get(t, Handler(), "GET", "/")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		t.Errorf("GET / = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

// TestAPIIsNotShadowed mounts the web client the way tend-server does:
// the owner API keeps everything under /api/.
func TestAPIIsNotShadowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "api "+r.URL.Path)
	}))
	mux.Handle("/", New(built()))
	for _, p := range []string{"/api/v1/session", "/api/v1/tasks/task_01ABC", "/api/unknown"} {
		if got := body(t, get(t, mux, "GET", p)); got != "api "+p {
			t.Errorf("GET %s = %q, want the API", p, got)
		}
	}
	if got := body(t, get(t, mux, "GET", "/tasks")); got != indexHTML {
		t.Errorf("GET /tasks = %q, want the web client", got)
	}
}
