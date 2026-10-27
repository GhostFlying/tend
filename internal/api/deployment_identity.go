package api

import (
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

const deploymentHeader = "X-Tend-Deployment-ID"

// deploymentGuard deliberately precedes token refresh: an old client cannot
// exchange credentials and replay an old mutation against an empty instance.
func (s *Server) deploymentGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.app.DeploymentID(r.Context())
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		w.Header().Set(deploymentHeader, id)
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.URL.Path != "/api/v1/session" && r.Header.Get(deploymentHeader) != id {
			writeJSON(w, http.StatusConflict, protocol.APIResponse{Error: protocol.Errorf(protocol.CodeInvalidRequest, "deployment identity missing or changed; upgrade or reconnect without replaying old requests")})
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) deployment(w http.ResponseWriter, r *http.Request) {
	if _, err := s.ownerLogin(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deployment_id": w.Header().Get(deploymentHeader)})
}
