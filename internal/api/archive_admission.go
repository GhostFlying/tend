package api

import (
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) archiveAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || r.URL.Path == "/api/v1/session" {
			next.ServeHTTP(w, r)
			return
		}
		if _, err := s.owner(r); err != nil {
			s.writeError(w, r, err)
			return
		}
		done, err := s.app.BeginOwnerMutation(r.Context())
		if err != nil {
			if protocol.CodeOf(err) == protocol.CodeInvalidTransition {
				if r.ProtoMajor == 1 {
					// Refuse this unadmitted connection without net/http draining
					// its body before the response or again during finishRequest.
					w.Header().Set("Connection", "close")
					_ = http.NewResponseController(w).SetReadDeadline(time.Now())
				}
				writeJSON(w, http.StatusServiceUnavailable, protocol.APIResponse{Error: protocol.Errorf(protocol.CodeInvalidTransition, "archive preparation rejects new owner writes; cancel preparation to resume")})
			} else {
				s.writeError(w, r, err)
			}
			return
		}
		defer done()
		next.ServeHTTP(w, r)
	})
}
