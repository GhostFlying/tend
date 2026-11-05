package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) VerifyCapability(ctx context.Context, p protocol.VerifyLocalCapabilityRequest) (protocol.LocalCapabilityRequestStatus, error) {
	var empty protocol.LocalCapabilityRequestStatus
	if !protocol.ValidID(protocol.PrefixRequest, p.RequestID) || p.ExpectedRevision < 1 || p.Pin.Validate(r.cfg.HostID) != nil || p.Pin.Purpose != "" || p.ProbeKind != "tool_inventory" || p.MaxAttempts != 1 || p.TimeoutMillis < 1 || p.TimeoutMillis > 30000 {
		return empty, protocol.Errorf(protocol.CodeInvalidRequest, "verification requires exact worker pin, original request/CAS, tool_inventory, max_attempts=1 and timeout_ms in 1..30000")
	}
	if _, err := protocol.CanonicalCapabilityReferences([]protocol.CapabilityReference{p.Capability}); err != nil {
		return empty, err
	}
	r.mu.Lock()
	origin := r.confirmedRuntime
	r.mu.Unlock()
	status, fresh, err := r.cfg.Journal.BeginCapabilityVerification(ctx, p, origin.InstanceID, origin.Generation)
	if err != nil || !fresh {
		return status, err
	}
	// Persisting before any work means cancellation, a crash or a lost local
	// response cannot cause this request to probe a second time.
	finish := func(state, code string, e *protocol.ExecutorCapabilityEvidence, env *protocol.Envelope) (protocol.LocalCapabilityRequestStatus, error) {
		status.State, status.ErrorCode, status.Evidence, status.Envelope = state, code, e, env
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return status, r.cfg.Journal.SaveCapabilityVerification(persistCtx, status)
	}
	r.controlMu.Lock()
	supported := r.executorCapabilitiesSupported
	r.controlMu.Unlock()
	if !supported || r.cfg.AgentInventory == nil {
		return finish("rejected", "executor_contract_not_negotiated", nil, nil)
	}
	runtime, err := r.inventoryRuntime()
	if err != nil {
		return finish("rejected", "inventory_not_negotiated", nil, nil)
	}
	local, err := r.cfg.AgentInventory(ctx)
	if err != nil {
		return finish("rejected", "configuration_unavailable", nil, nil)
	}
	retained := false
	for _, e := range runtime.AgentContinuations {
		if e.Pin == p.Pin && e.Available {
			retained = true
		}
	}
	if int64(local.Revision) != p.ExpectedRevision || !retained && (p.Pin.InventoryRevision != p.ExpectedRevision || p.Pin.SnapshotID != local.SnapshotID || p.Pin.PublicSHA256 != local.PublicSHA256) {
		return finish("rejected", "stale_revision", nil, nil)
	}
	server, err := r.readAgentInventory(ctx, runtime, "")
	if err != nil {
		return finish("rejected", "registration_unavailable", nil, nil)
	}
	if server.Current == nil || server.Current.LocalRevision != p.ExpectedRevision || server.Current.SnapshotID != local.SnapshotID || server.Current.PublicSHA256 != local.PublicSHA256 {
		return finish("rejected", "registration_changed", nil, nil)
	}
	resolver, ok := r.cfg.Executions.(CapabilityResolver)
	if !ok {
		return finish("rejected", "adapter_evidence_unsupported", nil, nil)
	}
	status.ProbeInstanceID, status.ProbeGeneration = runtime.InstanceID, runtime.Generation
	if err := r.cfg.Journal.SaveCapabilityVerification(ctx, status); err != nil {
		return status, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMillis)*time.Millisecond)
	e, err := resolver.ObserveCapability(probeCtx, p.Pin, p.Capability)
	cancel()
	now := r.cfg.Now().UnixMilli()
	if err != nil {
		e = protocol.ExecutorCapabilityEvidence{Pin: p.Pin, Capability: p.Capability, Kind: "tool_inventory", Scope: "profile", State: "unverified", Reason: "enumeration_incomplete"}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			e.Reason = "probe_timeout"
		}
	}
	if e.Pin != p.Pin || e.Capability != p.Capability || e.Kind != "tool_inventory" {
		return finish("rejected", "adapter_binding_mismatch", nil, nil)
	}
	e.InstanceID, e.Generation, e.Sequence = runtime.InstanceID, runtime.Generation, status.Sequence
	// The adapter's actual observation time is authoritative when supplied;
	// the runner may only shorten its TTL, never refresh a cached inventory.
	if e.ObservedAtMillis == 0 {
		e.ObservedAtMillis = now
	}
	if e.ExpiresAtMillis == 0 {
		e.ExpiresAtMillis = e.ObservedAtMillis + protocol.CapabilityEvidenceTTLMillis
	}
	if err := e.Validate(now); err != nil {
		return finish("rejected", "invalid_adapter_evidence", nil, nil)
	}
	env, err := r.newEnvelope(protocol.OpRunnerCapabilityEvidence, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.ReportCapabilityEvidencePayload{Runtime: runtime, Evidence: e})
	if err != nil {
		return finish("unknown", "report_not_prepared", &e, nil)
	}
	env.RequestID = p.RequestID
	revision := server.Current.ServerRevision
	env.ExpectedControlRevision = &revision
	status, err = finish("report_pending", "", &e, env)
	if err != nil {
		return status, err
	}
	op, err := r.c.do(ctx, r.c.host(), env)
	if err != nil {
		return status, err
	}
	if op.Status == protocol.OpFailed {
		return finish("rejected", string(protocol.CodeOf(op.Error)), &e, env)
	}
	if op.Status != protocol.OpSucceeded {
		return status, &transportError{errors.New("capability evidence report is unresolved")}
	}
	var accepted protocol.ExecutorCapabilityEvidence
	if err := json.Unmarshal(op.Result, &accepted); err != nil {
		return status, &transportError{err}
	}
	if accepted.RequestID != p.RequestID || accepted.Pin != p.Pin || accepted.Capability != p.Capability {
		return status, protocol.Errorf(protocol.CodeConflictPayload, "capability evidence receipt differs from original request")
	}
	return finish("recorded", "", &accepted, env)
}

// ReconcileCapability only reads the original report. It never enumerates tools,
// starts a session, retries generation, or changes the original request payload.
func (r *Runner) ReconcileCapability(ctx context.Context, id string) (protocol.LocalCapabilityRequestStatus, error) {
	s, err := r.cfg.Journal.CapabilityVerification(ctx, id)
	if err != nil || s.State == "recorded" || s.State == "rejected" {
		return s, err
	}
	if s.Envelope == nil {
		r.mu.Lock()
		runtime := r.confirmedRuntime
		r.mu.Unlock()
		if s.ProbeGeneration > 0 && runtime.Generation > s.ProbeGeneration && runtime.InstanceID != s.ProbeInstanceID {
			s.State = "interrupted"
			s.Recovery = "the original probe-only incarnation was fenced; explicitly submit a new request after fixing this entry"
			return s, r.cfg.Journal.SaveCapabilityVerification(ctx, s)
		}
		return s, nil
	}
	runtime, err := r.inventoryRuntime()
	if err != nil {
		return s, err
	}
	env, err := r.newEnvelope(protocol.OpRunnerCapabilityEvidenceStatus, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.AgentInventoryStatusPayload{Runtime: runtime, RequestID: id})
	if err != nil {
		return s, err
	}
	op, err := r.c.do(ctx, r.c.host(), env)
	if err != nil {
		return s, err
	}
	if op.Error != nil {
		return s, op.Error
	}
	var result protocol.CapabilityEvidenceRequestStatus
	if err := json.Unmarshal(op.Result, &result); err != nil {
		return s, &transportError{err}
	}
	if result.RequestState == "not_recorded" && runtime.InstanceID != s.ProbeInstanceID && runtime.Generation > s.ProbeGeneration {
		s.State = "not_recorded"
		s.Recovery = "the reporting incarnation was fenced and its original request was not recorded; explicitly create a new verification if still needed"
		return s, r.cfg.Journal.SaveCapabilityVerification(ctx, s)
	}
	if result.RequestState != "recorded" || result.Request == nil {
		return s, nil
	}
	hash, err := protocol.PayloadHash(s.Envelope)
	if err != nil {
		return s, err
	}
	if result.Request.RequestID != id || result.RequestPayloadHash != hash {
		return s, protocol.Errorf(protocol.CodeConflictPayload, "capability reconciliation differs from original request")
	}
	if result.Request.Status == protocol.OpFailed {
		s.State = "rejected"
		s.ErrorCode = string(protocol.CodeOf(result.Request.Error))
	} else if result.Request.Status == protocol.OpSucceeded {
		var e protocol.ExecutorCapabilityEvidence
		if err := json.Unmarshal(result.Request.Result, &e); err != nil {
			return s, &transportError{err}
		}
		s.State, s.Evidence = "recorded", &e
	}
	return s, r.cfg.Journal.SaveCapabilityVerification(ctx, s)
}

func decodeCapabilityControl(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid capability control input")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return protocol.Errorf(protocol.CodeInvalidRequest, "capability control requires one JSON object")
	}
	return nil
}

func (r *Runner) CapabilityHandler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, s protocol.LocalCapabilityRequestStatus, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			if s.ErrorCode == "" {
				s.ErrorCode = string(protocol.CodeOf(err))
			}
		}
		_ = json.NewEncoder(w).Encode(s)
	}
	mux.HandleFunc("POST /agents/capabilities/verify", func(w http.ResponseWriter, q *http.Request) {
		var p protocol.VerifyLocalCapabilityRequest
		if err := decodeCapabilityControl(w, q, &p); err != nil {
			respond(w, protocol.LocalCapabilityRequestStatus{}, err)
			return
		}
		s, err := r.VerifyCapability(q.Context(), p)
		respond(w, s, err)
	})
	mux.HandleFunc("GET /agents/capabilities/requests/{id}", func(w http.ResponseWriter, q *http.Request) {
		s, err := r.cfg.Journal.CapabilityVerification(q.Context(), q.PathValue("id"))
		respond(w, s, err)
	})
	mux.HandleFunc("POST /agents/capabilities/reconcile", func(w http.ResponseWriter, q *http.Request) {
		var p protocol.ReconcileLocalCapabilityRequest
		if err := decodeCapabilityControl(w, q, &p); err != nil {
			respond(w, protocol.LocalCapabilityRequestStatus{}, err)
			return
		}
		s, err := r.ReconcileCapability(q.Context(), p.RequestID)
		respond(w, s, err)
	})
	mux.Handle("/", next)
	return mux
}
