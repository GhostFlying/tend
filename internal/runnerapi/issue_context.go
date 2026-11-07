package runnerapi

import (
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) context(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	query, err := protocol.ParseIssueMaterialsQuery(r.URL.Query())
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	value, err := s.app.ReadRunContext(r.Context(), p, query)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) contextFileMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	value, err := s.app.ReadRunContextFile(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) downloadContextFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	file, body, err := s.app.OpenRunContextFile(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Name}))
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, body)
}
