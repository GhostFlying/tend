// Package webui serves the web client (D70). `make web` builds the web/
// project into dist/app, which is embedded into the binary; the server
// serves it at / next to the owner API under /api/, on the same origin.
// Without a build the embedded directory holds only a placeholder and every
// page is a short note saying the web client was not built, so building and
// testing the Go code never needs Node.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// dist holds the build output under dist/app. The committed placeholder
// keeps the pattern matching when nothing was built.
//
//go:embed all:dist
var dist embed.FS

//go:embed notbuilt.html
var notBuilt []byte

// reserved are the path prefixes other handlers own. A request under them
// that reaches the web client is a routing mistake and gets a 404, not the
// app's page, so an API client never mistakes HTML for an answer.
var reserved = []string{"/api/", "/runner/"}

// contentSecurityPolicy limits the app to its own origin: scripts, styles
// and API calls all come from the server that serves it.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; " +
	"connect-src 'self'; font-src 'self'; manifest-src 'self'; worker-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// notBuiltPolicy allows the placeholder page's inline style and nothing else.
const notBuiltPolicy = "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'"

// immutable is the cache policy of the build's hashed assets.
const immutable = "public, max-age=31536000, immutable"

// contentTypes covers extensions the standard table may lack.
var contentTypes = map[string]string{
	".webmanifest": "application/manifest+json",
	".js":          "text/javascript; charset=utf-8",
	".svg":         "image/svg+xml",
}

// file is one file of the build, read once at startup.
type file struct {
	data        []byte
	etag        string
	contentType string
}

type handler struct {
	files map[string]file
	index *file
}

// Handler serves the embedded build.
func Handler() http.Handler {
	app, err := fs.Sub(dist, "dist/app")
	if err != nil {
		// fs.Sub only rejects invalid paths, and this one is constant.
		panic(err)
	}
	return New(app)
}

// New serves the web client built into app. Paths of files in app are
// served as they are; other paths without an extension are the app's own
// routes and get index.html. Without index.html in app, every page is the
// not-built page.
func New(app fs.FS) http.Handler {
	h := &handler{files: map[string]file{}}
	_ = fs.WalkDir(app, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			// A missing build is not an error: it serves the placeholder.
			return nil
		}
		data, err := fs.ReadFile(app, name)
		if err != nil {
			return nil
		}
		h.files[name] = newFile(name, data)
		return nil
	})
	if f, ok := h.files["index.html"]; ok {
		h.index = &f
	}
	return h
}

func newFile(name string, data []byte) file {
	sum := sha256.Sum256(data)
	ext := path.Ext(name)
	ct, ok := contentTypes[ext]
	if !ok {
		ct = mime.TypeByExtension(ext)
	}
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return file{data: data, etag: `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`, contentType: ct}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	for _, prefix := range reserved {
		if p+"/" == prefix || strings.HasPrefix(p, prefix) {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h.index == nil {
		w.Header().Set("Content-Security-Policy", notBuiltPolicy)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(notBuilt))
		return
	}
	name := strings.TrimPrefix(p, "/")
	f, ok := h.files[name]
	switch {
	case ok:
	case path.Ext(name) == "":
		// An app route, such as one the browser was reloaded on.
		f, name = *h.index, "index.html"
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	if strings.HasPrefix(name, "assets/") {
		// Vite names these by their content hash.
		w.Header().Set("Cache-Control", immutable)
	} else {
		// index.html, the service worker and the manifest must be checked
		// on every load so a new build takes effect.
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Type", f.contentType)
	w.Header().Set("ETag", f.etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
}
