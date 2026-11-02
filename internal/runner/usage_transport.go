package runner

import (
	"encoding/json"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
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
	op, err := r.c.do(q.Context(), r.c.host(), &env)
	if err != nil {
		http.Error(w, "usage server is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &op})
}
