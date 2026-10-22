package runnerapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	limit := s.cfg.FileLimit
	if limit <= 0 {
		limit = app.DefaultFileLimit
	}
	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidRequest, "file exceeds upload limit or cannot be read"))
		return
	}
	digest := sha256.Sum256(content)
	manifest := protocol.ArtifactFile{Name: r.URL.Query().Get("name"), Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	payload, err := json.Marshal(manifest)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	env := protocol.Envelope{RequestID: r.URL.Query().Get("request_id"), Operation: app.OpRunsUploadFile, Target: protocol.Target{Type: "run", ID: p.Subject}, Payload: payload}
	op, err := s.app.Execute(r.Context(), p, &env, app.UploadFile{Content: content, FileLimit: limit, ResultLimit: s.cfg.ResultFileLimit})
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
	f, err := s.app.DeliverableFile(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(f.Content)
}
