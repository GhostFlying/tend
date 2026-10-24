package runnerapi

import (
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	if p.Kind != protocol.PrincipalRun || p.Role != protocol.RoleImplement {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeForbidden, "only an implementation run may upload files"))
		return
	}
	env := protocol.Envelope{RequestID: r.URL.Query().Get("request_id"), Operation: app.OpRunsUploadFile}
	op, err := s.app.UploadDeliverableFile(r.Context(), p, env.RequestID, r.URL.Query().Get("name"), r.Body)
	if err != nil {
		s.writeError(w, r, &env, err)
		return
	}
	writeJSON(w, http.StatusOK, response{Operation: &op})
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	f, body, err := s.app.OpenRunFile(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = io.Copy(w, body)
}
