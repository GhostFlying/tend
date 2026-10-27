package api

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
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
	media, disposition := "application/octet-stream", "attachment"
	if r.URL.Query().Get("inline") == "1" {
		var header [512]byte
		n, err := body.ReadAt(header[:], 0)
		if err != nil && err != io.EOF {
			s.writeError(w, r, err)
			return
		}
		switch detected := http.DetectContentType(header[:n]); detected {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
			media, disposition = detected, "inline"
		default:
			if ext := strings.ToLower(path.Ext(f.Name)); ext == ".html" || ext == ".htm" {
				media, disposition = "text/html; charset=utf-8", "inline"
			}
		}
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-SHA256", f.SHA256)
	// Active documents get a unique origin and no network or parent capabilities,
	// including when their signed URL is opened outside the preview iframe.
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'")
	http.ServeContent(w, r, f.Name, time.Time{}, body)
}

func (s *Server) fileMetadata(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, f.ArtifactFile)
}

func (s *Server) attachmentMetadata(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	f, err := s.app.ConversationAttachment(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, f)
}
