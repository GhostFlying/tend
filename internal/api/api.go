// Package api is the owner's HTTP API (D43): requests from the web client,
// authenticated as an owner (D42). Writes go through app like every other
// caller's; reads use views.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// maxBody bounds a request body.
const maxBody = 1 << 20

// Config configures the API. Zero values select the defaults.
type Config struct {
	// Owners are the tailnet logins allowed to use the API.
	Owners []string
	// SessionTTL is how long a session lasts. Default 30 days.
	SessionTTL time.Duration
	// Now is the clock. Default time.Now.
	Now func() time.Time
}

// Server serves the owner API.
type Server struct {
	app   *app.App
	store *store.Store
	whois netx.WhoIs
	key   []byte
	cfg   Config
	log   *slog.Logger
}

// New returns a Server. The store is used for reads only; secret is the
// server secret sessions are signed with.
func New(a *app.App, s *store.Store, whois netx.WhoIs, secret []byte, cfg Config, log *slog.Logger) *Server {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Server{app: a, store: s, whois: whois, key: sessionKey(secret), cfg: cfg, log: log}
}

// Handler returns the API's routes, all under /api/v1/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/session", s.createSession)
	mux.HandleFunc("GET /api/v1/session", s.readSession)
	mux.HandleFunc("POST /api/v1/ops", s.ops)
	return mux
}

var statusOf = map[protocol.Code]int{
	protocol.CodeInvalidRequest:   http.StatusBadRequest,
	protocol.CodeMissingRequestID: http.StatusBadRequest,
	protocol.CodeConflictPayload:  http.StatusConflict,
	protocol.CodeUnauthenticated:  http.StatusUnauthorized,
	protocol.CodeForbidden:        http.StatusForbidden,
	protocol.CodeNotFound:         http.StatusNotFound,
}

// writeError answers with err, logging internal errors with attrs.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error, attrs ...any) {
	pe, ok := errors.AsType[*protocol.Error](err)
	if !ok {
		s.log.ErrorContext(r.Context(), "owner request failed", append([]any{"path", r.URL.Path, "err", err}, attrs...)...)
		pe = protocol.Errorf(protocol.CodeInternal, "internal error")
	}
	status, ok := statusOf[pe.Code]
	if !ok {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, protocol.APIResponse{Error: pe})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
