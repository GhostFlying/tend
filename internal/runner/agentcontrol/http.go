package agentcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
)

// Handler is mounted on the existing runner-private control listener.
func (c *Control) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agents/status", func(w http.ResponseWriter, r *http.Request) {
		c.respond(w, r.Context(), protocol.LocalAgentConfigurationResult{}, nil)
	})
	mux.HandleFunc("POST /agents/validate", func(w http.ResponseWriter, r *http.Request) {
		p, err := c.Validate(r.Context())
		result := protocol.LocalAgentConfigurationResult{}
		if err == nil {
			result.Candidate = &p
		}
		c.respond(w, r.Context(), result, err)
	})
	mux.HandleFunc("POST /agents/reload", func(w http.ResponseWriter, r *http.Request) {
		var input protocol.ReloadLocalAgentsRequest
		if err := decodeRequest(w, r, &input); err != nil {
			c.respond(w, r.Context(), protocol.LocalAgentConfigurationResult{ErrorCode: "invalid_request"}, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		receipt, err := c.Reload(ctx, input)
		result := protocol.LocalAgentConfigurationResult{}
		if err == nil {
			result.Receipt = &receipt
			result.RequestState = "accepted"
		}
		c.respond(w, r.Context(), result, err)
	})
	mux.HandleFunc("POST /agents/reconcile", func(w http.ResponseWriter, r *http.Request) {
		var input protocol.ReconcileLocalAgentsRequest
		if err := decodeRequest(w, r, &input); err != nil {
			c.respond(w, r.Context(), protocol.LocalAgentConfigurationResult{ErrorCode: "invalid_request"}, err)
			return
		}
		state, receipt, err := c.Reconcile(r.Context(), input.RequestID)
		c.respond(w, r.Context(), protocol.LocalAgentConfigurationResult{RequestState: state, Receipt: receipt}, err)
	})
	return mux
}

func decodeRequest(w http.ResponseWriter, r *http.Request, value any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("invalid request body")
	}
	return nil
}

func (c *Control) respond(w http.ResponseWriter, ctx context.Context, result protocol.LocalAgentConfigurationResult, err error) {
	w.Header().Set("Content-Type", "application/json")
	current, statusErr := c.Status(ctx)
	if statusErr == nil {
		result.Current = &current
	}
	if err == nil {
		err = statusErr
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusConflict
		if result.ErrorCode == "" {
			result.ErrorCode = safeCode(err)
		}
	}
	// Response values contain no private references.
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func safeCode(err error) string {
	if errors.Is(err, errSourceUnreadable) {
		return "source_unreadable"
	}
	if errors.Is(err, errHostedRuntime) {
		return "hosted_runtime_not_supported"
	}
	if e, ok := errors.AsType[*agentregistry.Error](err); ok {
		return e.Code
	}
	if e, ok := errors.AsType[*agentconfig.ValidationError](err); ok {
		return "invalid_definition_" + e.Code
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "configuration_unavailable"
}
