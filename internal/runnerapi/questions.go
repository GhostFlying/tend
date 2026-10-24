package runnerapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Questions are run-scoped durable facts. Waiting releases every DB read
// transaction and may be repeated after any transport or process interruption.
func (s *Server) questions(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	if p.Kind != protocol.PrincipalRun {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeForbidden, "run identity is required"))
		return
	}
	id := r.PathValue("id")
	if id == "" {
		questions, err := s.app.WorkerQuestions(r.Context(), p)
		if err != nil {
			s.writeError(w, r, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, questions)
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
	deadline := time.NewTimer(time.Duration(wait) * time.Second)
	defer deadline.Stop()
	for {
		changed := s.app.Changes().Wait()
		question, err := s.app.WorkerQuestion(r.Context(), p, id)
		if err != nil {
			s.writeError(w, r, nil, err)
			return
		}
		if question.Status != "open" || wait == 0 {
			writeJSON(w, http.StatusOK, question)
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			// Re-read once at the timeout boundary so the response reflects
			// an answer or cancellation committed while the timer fired.
			wait = 0
		case <-r.Context().Done():
			return
		}
	}
}
