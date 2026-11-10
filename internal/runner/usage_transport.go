package runner

import (
	"encoding/json"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// localUsageRPC reuses the running runner's authenticated networking on its
// private control socket. It cannot proxy business operations or arbitrary URLs.
func (r *Runner) localUsageRPC(w http.ResponseWriter, q *http.Request) {
	if !r.supportsNativeUsage() {
		http.Error(w, "native usage capability is pending", http.StatusServiceUnavailable)
		return
	}
	var env protocol.Envelope
	decoder := json.NewDecoder(http.MaxBytesReader(w, q.Body, 16<<20))
	if err := decoder.Decode(&env); err != nil {
		http.Error(w, "invalid usage envelope", http.StatusBadRequest)
		return
	}
	if env.Target.Type != "host" || env.Target.ID != r.cfg.HostID {
		http.Error(w, "usage host differs", http.StatusForbidden)
		return
	}
	switch env.Operation {
	case protocol.OpRunnerUsageRegister, protocol.OpRunnerUsageSources, protocol.OpRunnerUsagePreview, protocol.OpRunnerUsageUpload:
	default:
		http.Error(w, "only native usage operations are allowed", http.StatusForbidden)
		return
	}
	if env.Operation == protocol.OpRunnerUsageUpload {
		pending, err := r.cfg.Journal.Requests(q.Context())
		if err != nil {
			http.Error(w, "usage journal unavailable", http.StatusServiceUnavailable)
			return
		}
		for _, request := range pending {
			if request.Envelope.RequestID != env.RequestID {
				continue
			}
			want, werr := protocol.PayloadHash(&request.Envelope)
			got, gerr := protocol.PayloadHash(&env)
			if werr != nil || gerr != nil || want != got {
				http.Error(w, "usage request identity changed", http.StatusConflict)
				return
			}
		}
	}
	op, err := r.c.do(q.Context(), r.c.host(), &env)
	if err != nil {
		http.Error(w, "usage server is unavailable", http.StatusServiceUnavailable)
		return
	}
	if env.Operation == protocol.OpRunnerUsageUpload && op.Status == protocol.OpSucceeded && op.Error == nil {
		if err := r.cfg.Journal.DeleteRequest(q.Context(), env.RequestID); err != nil {
			http.Error(w, "usage acknowledgement remains pending", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &op})
}

// localUsageJournal keeps foreground usage maintenance in the live runner's
// single writer. Existing pending envelopes and cursor commits stay together.
func (r *Runner) localUsageJournal(w http.ResponseWriter, q *http.Request) {
	if !r.supportsNativeUsage() {
		http.Error(w, "native usage capability is pending", http.StatusServiceUnavailable)
		return
	}
	if q.URL.Path == "/runner/v1/usage/pending" {
		if q.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		requests, err := r.cfg.Journal.Requests(q.Context())
		if err != nil {
			http.Error(w, "usage journal unavailable", http.StatusServiceUnavailable)
			return
		}
		pending := []journal.Request{}
		for _, request := range requests {
			if request.Envelope.Operation == protocol.OpRunnerUsageUpload {
				pending = append(pending, request)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pending)
		return
	}
	if q.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, q.Body, 16<<20))
	decoder.DisallowUnknownFields()
	var err error
	var result journal.UsageSourceSnapshot
	stage, sourceID, runID, threadID, requestID := "queue_usage", "", "", "", ""
	if q.URL.Path == "/runner/v1/usage/source" {
		stage = "register_source"
		var source journal.UsageSource
		if err = decoder.Decode(&source); err == nil {
			sourceID, runID, threadID = source.ID, source.RunID, source.Source.ThreadID
			if source.ID == "" {
				http.Error(w, "invalid usage source", http.StatusBadRequest)
				return
			}
			result, err = r.cfg.Journal.RegisterUsageMaintenanceSource(q.Context(), source, false)
		}
	} else {
		var input journal.UsageQueueRequest
		if err = decoder.Decode(&input); err == nil {
			sourceID, runID, threadID = input.Source.ID, input.Source.RunID, input.Source.Source.ThreadID
			if input.Envelope != nil {
				requestID = input.Envelope.RequestID
			}
			if input.Source.ID == "" || input.Envelope != nil && (input.Envelope.RequestID == "" || input.Envelope.Operation != protocol.OpRunnerUsageUpload || input.Envelope.Target.Type != "host" || input.Envelope.Target.ID != r.cfg.HostID) {
				http.Error(w, "invalid usage upload binding", http.StatusBadRequest)
				return
			}
			input.Source.ExpectedRevision = input.ExpectedRevision
			result, err = r.cfg.Journal.QueueUsageSnapshot(q.Context(), input.Source, input.Envelope)
		}
	}
	if err != nil {
		code := protocol.CodeOf(err)
		details, _ := json.Marshal(map[string]string{"stage": stage, "source_id": sourceID, "run_id": runID, "thread_id": threadID, "request_id": requestID})
		// Return only a stable code and the attempted binding, never SQL errors,
		// original content, credentials or the caller-controlled error message.
		if r.cfg.Log != nil {
			r.cfg.Log.Warn("usage journal operation rejected", "stage", stage, "source_id", sourceID, "run_id", runID, "thread_id", threadID, "request_id", requestID, "code", code)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Error: &protocol.Error{Code: code, Message: "usage journal operation rejected", Details: details}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
