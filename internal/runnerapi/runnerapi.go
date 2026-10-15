// Package runnerapi serves the runner protocol over HTTP on the tailnet.
//
// Every request is a POST of an envelope to /runner/v1/rpc. A runner acts
// with its host credential: X-Tend-Host names the host and the bearer token
// must hash to the host's configured token hash. A run acts with its own
// credential: X-Tend-Run names the run and the bearer token is derived from
// the run's lease with the server secret (D29), so tokens are never stored.
// In both cases the tailnet node must be one the host is configured with;
// the tailnet identity alone never grants a role.
package runnerapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Path is the single endpoint of the runner protocol.
const Path = "/runner/v1/rpc"

// Request headers naming the credential's subject.
const (
	HeaderHost = "X-Tend-Host"
	HeaderRun  = "X-Tend-Run"
)

// maxBody bounds a request body; event batches are the largest requests.
const maxBody = 4 << 20

// RunToken derives a run's credential from its lease.
func RunToken(secret []byte, leaseID string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(leaseID))
	return hex.EncodeToString(m.Sum(nil))
}

// HashToken returns the hash of a host token as it appears in the
// configuration.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Broadcast wakes every waiting claim when the ledger changes. It
// implements app.Notifier.
type Broadcast struct {
	mu sync.Mutex
	ch chan struct{}
}

// NewBroadcast returns a Broadcast.
func NewBroadcast() *Broadcast { return &Broadcast{ch: make(chan struct{})} }

// Notify implements app.Notifier.
func (b *Broadcast) Notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	close(b.ch)
	b.ch = make(chan struct{})
}

// wait returns a channel that is closed by the next Notify.
func (b *Broadcast) wait() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ch
}

// Config tunes the server. Zero values select the defaults.
type Config struct {
	// ClaimWait bounds how long a claim waits for work. Default 25s.
	ClaimWait time.Duration
}

// Server handles runner requests.
type Server struct {
	app     *app.App
	whois   netx.WhoIs
	secret  []byte
	changes *Broadcast
	cfg     Config
	log     *slog.Logger
}

// New returns a Server. changes must be the Broadcast the App notifies.
func New(a *app.App, whois netx.WhoIs, secret []byte, changes *Broadcast, cfg Config, log *slog.Logger) *Server {
	if cfg.ClaimWait <= 0 {
		cfg.ClaimWait = 25 * time.Second
	}
	return &Server{app: a, whois: whois, secret: secret, changes: changes, cfg: cfg, log: log}
}

// Handler returns the HTTP handler of the protocol.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path, s.serve)
	return mux
}

type response struct {
	Operation *protocol.Operation `json:"operation,omitempty"`
	Error     *protocol.Error     `json:"error,omitempty"`
}

var statusOf = map[protocol.Code]int{
	protocol.CodeInvalidRequest:   http.StatusBadRequest,
	protocol.CodeMissingRequestID: http.StatusBadRequest,
	protocol.CodeConflictPayload:  http.StatusConflict,
	protocol.CodeForbidden:        http.StatusForbidden,
	protocol.CodeNotFound:         http.StatusNotFound,
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, env *protocol.Envelope, err error) {
	pe, ok := errors.AsType[*protocol.Error](err)
	if !ok {
		attrs := []any{"err", err}
		if env != nil {
			attrs = append(attrs, "request_id", env.RequestID, "operation", env.Operation)
		}
		s.log.ErrorContext(r.Context(), "runner request failed", attrs...)
		pe = protocol.Errorf(protocol.CodeInternal, "internal error")
	}
	status, ok := statusOf[pe.Code]
	if !ok {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, response{Error: pe})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var errUnauthenticated = protocol.Errorf(protocol.CodeForbidden, "authentication failed")

// authenticate establishes the principal of a request.
func (s *Server) authenticate(r *http.Request) (protocol.Principal, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return protocol.Principal{}, errUnauthenticated
	}
	ctx := r.Context()
	id, err := s.whois.WhoIs(ctx, r.RemoteAddr)
	if err != nil {
		s.log.WarnContext(ctx, "runner request from unknown peer", "remote", r.RemoteAddr, "err", err)
		return protocol.Principal{}, errUnauthenticated
	}
	var (
		p      protocol.Principal
		hostID string
	)
	switch runID, hostHeader := r.Header.Get(HeaderRun), r.Header.Get(HeaderHost); {
	case runID != "" && hostHeader == "":
		leaseID, host, role, err := s.app.RunCredential(ctx, runID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return protocol.Principal{}, errUnauthenticated
			}
			return protocol.Principal{}, err
		}
		if !hmac.Equal([]byte(token), []byte(RunToken(s.secret, leaseID))) {
			return protocol.Principal{}, errUnauthenticated
		}
		p, hostID = protocol.Principal{Kind: protocol.PrincipalRun, Subject: runID, Role: role}, host
	case hostHeader != "" && runID == "":
		p, hostID = protocol.Principal{Kind: protocol.PrincipalRunner, Subject: hostHeader}, hostHeader
	default:
		return protocol.Principal{}, errUnauthenticated
	}
	host, err := s.app.Host(ctx, hostID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return protocol.Principal{}, errUnauthenticated
		}
		return protocol.Principal{}, err
	}
	if p.Kind == protocol.PrincipalRunner && subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(host.TokenHash)) != 1 {
		return protocol.Principal{}, errUnauthenticated
	}
	if host.Status != store.HostActive || !id.Matches(host.Nodes) {
		s.log.WarnContext(ctx, "runner credential used from an unexpected node", "host_id", host.HostID, "node", id.Node)
		return protocol.Principal{}, errUnauthenticated
	}
	return p, nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	var env protocol.Envelope
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&env); err != nil {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidRequest, "body is not an envelope"))
		return
	}
	if err := env.Validate(); err != nil {
		s.writeError(w, r, &env, err)
		return
	}
	op, err := s.dispatch(r.Context(), p, &env)
	if err != nil {
		s.writeError(w, r, &env, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Operation: &op})
}

// runCommands are the operations a run performs through the ledger.
var runCommands = map[string]app.Command{
	protocol.OpRunsReportWorkspace: app.ReportWorkspace{},
	protocol.OpRunsReportStarted:   app.ReportRunStarted{},
	protocol.OpRunsSubmitArtifact:  app.SubmitArtifact{},
	protocol.OpRunsRelease:         app.Release{},
	protocol.OpAcceptancesSubmit:   app.SubmitAcceptance{},
}

func (s *Server) dispatch(ctx context.Context, p protocol.Principal, env *protocol.Envelope) (protocol.Operation, error) {
	switch env.Operation {
	case protocol.OpRunnerHello:
		op, err := s.app.Execute(ctx, p, env, app.Hello{})
		if err != nil || op.Status != protocol.OpSucceeded {
			return op, err
		}
		var res protocol.HelloResult
		if err := json.Unmarshal(op.Result, &res); err != nil {
			return op, err
		}
		for i := range res.Reassigned {
			res.Reassigned[i].RunToken = RunToken(s.secret, res.Reassigned[i].LeaseID)
		}
		return withResult(op, res)
	case protocol.OpRunnerClaim:
		return s.claim(ctx, p, env)
	case protocol.OpRunnerHeartbeat:
		var pl protocol.HeartbeatPayload
		if err := decodeStrict(env.Payload, &pl); err != nil {
			return protocol.Operation{}, err
		}
		res, err := s.app.Heartbeat(ctx, p, pl.Runs)
		if err != nil {
			return protocol.Operation{}, err
		}
		return withResult(protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}, res)
	case protocol.OpRunnerEvents:
		var pl protocol.EventsPayload
		if err := decodeStrict(env.Payload, &pl); err != nil {
			return protocol.Operation{}, err
		}
		res, err := s.app.RecordRunEvents(ctx, p, env.Target.ID, pl.Events)
		if err != nil {
			return protocol.Operation{}, err
		}
		return withResult(protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}, res)
	}
	if cmd, ok := runCommands[env.Operation]; ok {
		return s.app.Execute(ctx, p, env, cmd)
	}
	return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "operation %q is not part of the runner protocol", env.Operation)
}

// claim waits up to ClaimWait for work. A claim that finds nothing has no
// effect, so it is safe to run again under the same request ID whenever
// the ledger changes.
func (s *Server) claim(ctx context.Context, p protocol.Principal, env *protocol.Envelope) (protocol.Operation, error) {
	deadline := time.NewTimer(s.cfg.ClaimWait)
	defer deadline.Stop()
	for {
		changed := s.changes.wait()
		op, err := s.app.Execute(ctx, p, env, app.Claim{})
		if err != nil || op.Status != protocol.OpSucceeded {
			return op, err
		}
		var res struct {
			Assignment *protocol.Assignment `json:"assignment"`
		}
		if err := json.Unmarshal(op.Result, &res); err != nil {
			return op, err
		}
		if res.Assignment != nil {
			res.Assignment.RunToken = RunToken(s.secret, res.Assignment.LeaseID)
			return withResult(op, res)
		}
		select {
		case <-changed:
		case <-deadline.C:
			return op, nil
		case <-ctx.Done():
			return protocol.Operation{}, ctx.Err()
		}
	}
}

func withResult(op protocol.Operation, v any) (protocol.Operation, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return protocol.Operation{}, err
	}
	op.Result = b
	return op, nil
}

func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "payload is malformed")
	}
	return nil
}

// SweepLeases expires lapsed leases every interval until ctx is cancelled.
func (s *Server) SweepLeases(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n, err := s.app.ExpireLeases(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "lease sweep failed", "err", err)
		} else if n > 0 {
			s.log.InfoContext(ctx, "leases expired", "count", n)
		}
	}
}
