package runnerapi

import (
	"io"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) extensionContent(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	if p.Kind != protocol.PrincipalRunner {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeForbidden, "only runners download extensions"))
		return
	}
	body, err := s.app.OpenExtensionContent(r.Context(), p, r.PathValue("release"), r.PathValue("sha"))
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, body)
}
