package api

import (
	"io"
	"mime"
	"net/http"
	"strconv"
)

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.filePrincipal(r, "deliverable")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	f, body, err := s.app.OpenDeliverableFile(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
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
