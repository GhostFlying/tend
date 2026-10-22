package api

import (
	"mime"
	"net/http"
	"strconv"
)

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	f, err := s.app.DeliverableFile(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(f.Content)
}
