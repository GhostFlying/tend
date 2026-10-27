package runnerapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) runMessages(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	if p.Kind != protocol.PrincipalRun {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeForbidden, "target run identity is required"))
		return
	}
	wait := 0
	if raw := r.URL.Query().Get("wait"); raw != "" {
		wait, err = strconv.Atoi(raw)
		if err != nil || wait < 0 || wait > 25 {
			s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidRequest, "wait must be between 0 and 25 seconds"))
			return
		}
	}
	timer := time.NewTimer(time.Duration(wait) * time.Second)
	defer timer.Stop()
	for {
		changed := s.app.Changes().Wait()
		page, err := s.app.RunMessages(r.Context(), p, p.Subject, 0, 1, true)
		if err != nil {
			s.writeError(w, r, nil, err)
			return
		}
		if len(page.Messages) > 0 || wait == 0 {
			writeJSON(w, http.StatusOK, page)
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			wait = 0
		case <-r.Context().Done():
			return
		}
	}
}
