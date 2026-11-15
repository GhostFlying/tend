package runnerapi

import (
	"encoding/json"
	"net/http"

	"github.com/GhostFlying/tend/internal/httpdiag"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) rolloutPreview(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		httpdiag.Waiting(r.Context())
		result, err := s.app.PollRolloutPreview(r.Context(), p, r.URL.Query().Get("instance"), s.cfg.Stopping)
		if err != nil {
			s.writeError(w, r, nil, err)
			return
		}
		if result == nil {
			select {
			case <-s.cfg.Stopping:
				httpdiag.WaitStopped(r.Context())
			default:
			}
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	var reply protocol.RolloutPreviewReply
	if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&reply); err != nil {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid preview reply"))
		return
	}
	if err = s.app.ReplyRolloutPreview(r.Context(), p, reply); err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
