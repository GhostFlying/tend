// Package api serves authenticated owner requests; all mutations go through app.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
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
	// OwnerNodes maps nodes, by machine name or "tag:", to the owner whose
	// devices they are. A tagged node has no login of its own (D56).
	OwnerNodes map[string]string
	// TokenTTL is how long a token lasts. Default 30 days.
	TokenTTL time.Duration
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// HostedMainAgent accepts the conversation operations, which only the
	// hosted main thread handles (D45).
	HostedMainAgent bool
	// ConversationSettings reads the hosted harness catalog and shared settings.
	ConversationSettings func(context.Context) (protocol.ConversationSettingsView, error)
	// ConversationHandoff reads policy and summary coverage independently of models.
	ConversationHandoff func(context.Context) (protocol.ConversationHandoffView, error)
	// Heartbeat is how often a quiet event stream sends a heartbeat.
	// Default 15s.
	Heartbeat time.Duration
}

// Server serves the owner API.
type Server struct {
	app   *app.App
	store *store.Store
	whois netx.WhoIs
	key   []byte
	cfg   Config
	log   *slog.Logger
	// ending is closed when the server shuts down, ending event streams.
	ending    chan struct{}
	endStream sync.Once
}

// EndStreams ends the event streams, for a server that is shutting down:
// unlike other requests they never finish by themselves.
func (s *Server) EndStreams() { s.endStream.Do(func() { close(s.ending) }) }

// New returns a Server. The store is used for reads only; secret is the
// server secret tokens are signed with.
func New(a *app.App, s *store.Store, whois netx.WhoIs, secret []byte, cfg Config, log *slog.Logger) *Server {
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = 30 * 24 * time.Hour
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 15 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Server{app: a, store: s, whois: whois, key: tokenKey(secret), cfg: cfg, log: log, ending: make(chan struct{})}
}

// Handler returns the API's routes, all under /api/v1/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/deployment", s.deployment)
	mux.HandleFunc("POST /api/v1/session", s.createSession)
	mux.HandleFunc("GET /api/v1/session", s.readSession)
	mux.HandleFunc("POST /api/v1/ops", s.ops)
	mux.HandleFunc("GET /api/v1/snapshot", s.snapshot)
	mux.HandleFunc("GET /api/v1/upgrades/{id}", s.upgrade)
	mux.HandleFunc("GET /api/v1/categories/{id}", s.agentCategory)
	mux.HandleFunc("GET /api/v1/repos", s.repos)
	mux.HandleFunc("GET /api/v1/repos/{id}", s.repository)
	mux.HandleFunc("GET /api/v1/tasks", s.tasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.task)
	mux.HandleFunc("GET /api/v1/work-item-choices/{id}", s.workItemChoice)
	mux.HandleFunc("GET /api/v1/work-items/{id}/followups", s.followups)
	mux.HandleFunc("GET /api/v1/followups/{id}/history", s.followupHistory)
	mux.HandleFunc("GET /api/v1/background/{id}/sources", s.backgroundSources)
	mux.HandleFunc("GET /api/v1/coordination/metrics", s.coordinationMetrics)
	mux.HandleFunc("GET /api/v1/token-usage", s.tokenUsage)
	mux.HandleFunc("GET /api/v1/work-items", s.workItems)
	mux.HandleFunc("GET /api/v1/work-items/{id}", s.workItem)
	mux.HandleFunc("GET /api/v1/files/{id}", s.downloadFile)
	mux.HandleFunc("GET /api/v1/files/{id}/metadata", s.fileMetadata)
	mux.HandleFunc("GET /api/v1/runs/{id}/logs", s.runLogs)
	mux.HandleFunc("GET /api/v1/runs/{id}/rollout", s.runRollout)
	mux.HandleFunc("GET /api/v1/requests/{id}", s.request)
	mux.HandleFunc("GET /api/v1/conversation", s.conversation)
	mux.HandleFunc("GET /api/v1/agent-conversations", s.agentConversations)
	mux.HandleFunc("GET /api/v1/agent-conversations/{id}", s.agentConversation)
	mux.HandleFunc("GET /api/v1/messages/{id}/associations", s.messageAssociations)
	mux.HandleFunc("GET /api/v1/work-items/{id}/context", s.workItemContext)
	mux.HandleFunc("GET /api/v1/agent-conversations/{id}/rollout", s.agentRollout)
	mux.HandleFunc("GET /api/v1/agent-conversations/{id}/rollout/{seq}", s.agentRolloutWindow)
	mux.HandleFunc("GET /api/v1/agent-conversations/{id}/usage", s.agentUsage)
	mux.HandleFunc("GET /api/v1/agent-conversations/{id}/compactions", s.agentCompactions)
	mux.HandleFunc("GET /api/v1/conversation/queue", s.conversationQueue)
	mux.HandleFunc("GET /api/v1/conversation/transcript", s.conversationTranscript)
	mux.HandleFunc("GET /api/v1/conversation/blocks/{id}", s.conversationBlock)
	mux.HandleFunc("GET /api/v1/conversation/images/{id}", s.conversationImage)
	mux.HandleFunc("POST /api/v1/conversation/attachments", s.uploadAttachment)
	mux.HandleFunc("GET /api/v1/conversation/attachments/{id}", s.downloadAttachment)
	mux.HandleFunc("GET /api/v1/conversation/attachments/{id}/metadata", s.attachmentMetadata)
	mux.HandleFunc("GET /api/v1/conversation/attachments/{id}/download-url", s.attachmentDownloadURL)
	mux.HandleFunc("GET /api/v1/conversation/images/{id}/content", s.downloadAttachment)
	mux.HandleFunc("GET /api/v1/conversation/images/{id}/download-url", s.attachmentDownloadURL)
	mux.HandleFunc("GET /api/v1/files/{id}/download-url", s.deliverableDownloadURL)
	mux.HandleFunc("GET /api/v1/conversation/settings", s.conversationSettings)
	mux.HandleFunc("GET /api/v1/conversation/handoff", s.conversationHandoff)
	mux.HandleFunc("GET /api/v1/events", s.events)
	return s.deploymentGuard(mux)
}

var statusOf = map[protocol.Code]int{
	protocol.CodeInvalidRequest:   http.StatusBadRequest,
	protocol.CodeMissingRequestID: http.StatusBadRequest,
	protocol.CodeConflictPayload:  http.StatusConflict,
	protocol.CodeUnauthenticated:  http.StatusUnauthorized,
	protocol.CodeForbidden:        http.StatusForbidden,
	protocol.CodeNotFound:         http.StatusNotFound,
}

// writeError answers with err as a protocol error. Errors that are not
// protocol errors are logged with attrs and hidden from the caller.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error, attrs ...any) {
	pe, ok := errors.AsType[*protocol.Error](err)
	if !ok {
		attrs = append([]any{"path", r.URL.Path, "err", err}, attrs...)
		if r.Context().Err() != nil {
			// The caller hung up; nothing failed.
			s.log.InfoContext(r.Context(), "owner request abandoned", attrs...)
		} else {
			s.log.ErrorContext(r.Context(), "owner request failed", attrs...)
		}
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
