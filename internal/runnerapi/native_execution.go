package runnerapi

import (
	"context"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) nativeExecution(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("channel") == "status" {
		p, err := s.authenticate(r)
		if err != nil || p.Kind != protocol.PrincipalRunner {
			http.Error(w, "authentication failed", http.StatusForbidden)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": s.cfg.NativeWorkers != nil})
		return
	}

	if s.cfg.NativeWorkers == nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.authenticate(r)
	if err != nil || p.Kind != protocol.PrincipalRun {
		http.Error(w, "authentication failed", http.StatusForbidden)
		return
	}
	s.cfg.NativeWorkers.Serve(w, r, p.Subject, r.PathValue("channel"), func(ctx context.Context) (protocol.RunDesired, error) {
		current, err := s.authenticate(r.WithContext(ctx))
		if err != nil {
			return "", err
		}
		return s.app.NativeExecutionControl(ctx, current)
	})
}
