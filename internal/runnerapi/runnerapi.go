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
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Prefix contains the RPC and binary file endpoints of the runner protocol.
const Prefix = "/runner/v1/"

// Path is the RPC endpoint of the runner protocol.
const Path = Prefix + "rpc"

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

// Config tunes the server. Zero values select the defaults.
type Config struct {
	ReleaseDir string
	// ClaimWait bounds how long a claim waits for work. Default 25s.
	ClaimWait time.Duration
	// Deprecated: these fields are accepted but no longer limit original files.
	FileLimit       int64
	ResultFileLimit int64
}

// Server handles runner requests.
type Server struct {
	app    *app.App
	whois  netx.WhoIs
	secret []byte
	cfg    Config
	log    *slog.Logger
}

// New returns a Server.
func New(a *app.App, whois netx.WhoIs, secret []byte, cfg Config, log *slog.Logger) *Server {
	if cfg.ClaimWait <= 0 {
		cfg.ClaimWait = 25 * time.Second
	}
	return &Server{app: a, whois: whois, secret: secret, cfg: cfg, log: log}
}

// Handler returns the HTTP handler of the protocol.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path, s.serve)
	mux.HandleFunc("GET /runner/v1/releases/{upgrade}/manifest", s.releaseFile)
	mux.HandleFunc("GET /runner/v1/releases/{upgrade}/artifacts/{sha}", s.releaseFile)
	mux.HandleFunc("POST /runner/v1/files", s.uploadFile)
	mux.HandleFunc("GET /runner/v1/files/{id}", s.downloadFile)
	mux.HandleFunc("GET /runner/v1/observation", s.observation)
	mux.HandleFunc("GET /runner/v1/questions", s.questions)
	mux.HandleFunc("GET /runner/v1/questions/{id}", s.questions)
	mux.HandleFunc("GET /runner/v1/messages", s.runMessages)
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
		if r.Context().Err() != nil {
			// The runner hung up, as it does when it stops; nothing failed.
			s.log.InfoContext(r.Context(), "runner request abandoned", attrs...)
		} else {
			s.log.ErrorContext(r.Context(), "runner request failed", attrs...)
		}
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
	return s.authenticateCredential(r, nil)
}

func (s *Server) authenticateCredential(r *http.Request, delivery *protocol.RunMessageDelivery) (protocol.Principal, error) {
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
		if delivery != nil {
			leaseID, host, role, err = s.app.RunMessageReceiptCredential(ctx, runID, *delivery)
		}
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
		if delivery != nil {
			return protocol.Principal{}, errUnauthenticated
		}
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
	p, authErr := s.authenticate(r)
	if authErr != nil && protocol.CodeOf(authErr) != protocol.CodeForbidden {
		s.writeError(w, r, nil, authErr)
		return
	}
	var env protocol.Envelope
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&env); err != nil {
		if authErr != nil {
			s.writeError(w, r, nil, authErr)
			return
		}
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidRequest, "body is not an envelope"))
		return
	}
	if err := env.Validate(); err != nil {
		if authErr != nil {
			s.writeError(w, r, nil, authErr)
			return
		}
		s.writeError(w, r, &env, err)
		return
	}
	if authErr != nil {
		// Historical run tokens only authenticate this exact RPC and delivery.
		// GETs and every other operation retain ordinary lease authentication.
		var receipt protocol.RunMessageReceipt
		if env.Operation == protocol.OpRunsReportMessage && env.Target.Type == "run" && env.Target.ID == r.Header.Get(HeaderRun) && json.Unmarshal(env.Payload, &receipt) == nil {
			p, authErr = s.authenticateCredential(r, &receipt.RunMessageDelivery)
		}
		if authErr != nil {
			s.writeError(w, r, &env, authErr)
			return
		}
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
	protocol.OpDeliveriesPoll:       app.PollDeliveries{},
	protocol.OpDeliveriesReport:     app.ReportDelivery{},
	protocol.OpRunsAskOwner:         app.AskOwner{},
	protocol.OpRunsClaimMessage:     app.ClaimRunMessage{},
	protocol.OpRunsReportMessage:    app.ReportRunMessage{},
	protocol.OpWorkspaceReconcile:   app.ReconcileWorkspaces{},
	protocol.OpWorkspaceRelease:     app.ReleaseWorkspace{},
	protocol.OpWorkspaceCleanup:     app.ReserveWorkspaceCleanup{},
	protocol.OpWorkspaceCleanupDone: app.CompleteWorkspaceCleanup{},
	protocol.OpRunsReportWorkspace:  app.ReportWorkspace{},
	protocol.OpRunsReportStarted:    app.ReportRunStarted{},
	protocol.OpRunsSubmitArtifact:   app.SubmitArtifact{},
	protocol.OpRunsRelease:          app.Release{},
	protocol.OpRunsReportTakeover:   app.ReportTakeover{},
	protocol.OpAcceptancesSubmit:    app.SubmitAcceptance{},
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
	case "runner.upgrade_authorize":
		op, err := s.app.Execute(ctx, p, env, app.AuthorizeRunnerUpgrade{})
		if err != nil || op.Status != protocol.OpSucceeded {
			return op, err
		}
		var grant protocol.UpgradeGrant
		if err := json.Unmarshal(op.Result, &grant); err != nil {
			return op, err
		}
		return withResult(op, s.app.RefreshUpgradeGrant(grant))
	case "runner.upgrade_health":
		var b protocol.UpgradeBinding
		if err := decodeStrict(env.Payload, &b); err != nil {
			return protocol.Operation{}, err
		}
		health, err := s.app.RunnerUpgradeHealth(ctx, p, b)
		if err != nil {
			return protocol.Operation{}, err
		}
		return withResult(protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}, health)
	case protocol.OpRunnerClaim:
		return s.claim(ctx, p, env)
	case protocol.OpRunnerHeartbeat:
		var pl protocol.HeartbeatPayload
		if err := decodeStrict(env.Payload, &pl); err != nil {
			return protocol.Operation{}, err
		}
		res, err := s.app.RuntimeHeartbeat(ctx, p, pl)
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
	// An idle runner is heard from only through its claims (D62). A host
	// that may not claim is refused by the claim itself.
	if err := s.app.SeeHost(ctx, p.Subject); err != nil {
		return protocol.Operation{}, err
	}
	deadline := time.NewTimer(s.cfg.ClaimWait)
	defer deadline.Stop()
	for {
		changed := s.app.Changes().Wait()
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

// SweepWorkspaces checks every interval, until ctx is cancelled, for
// tasks whose workspace host became unavailable or came back (D62).
func (s *Server) SweepWorkspaces(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if tasks, err := s.app.CheckWorkspaces(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "workspace check failed", "err", err)
		} else if len(tasks) > 0 {
			s.log.InfoContext(ctx, "workspace availability changed", "tasks", tasks)
		}
	}
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
		if _, err := s.app.SweepRunnerPresence(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "runner presence sweep failed", "err", err)
		}
		if n, err := s.app.ExpireLeases(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "lease sweep failed", "err", err)
		} else if n > 0 {
			s.log.InfoContext(ctx, "leases expired", "count", n)
		}
	}
}
