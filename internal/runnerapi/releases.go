package runnerapi

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func (s *Server) releaseFile(w http.ResponseWriter, r *http.Request) {
	s.releaseMetadata(w, r, false)
}

func (s *Server) releaseSetFile(w http.ResponseWriter, r *http.Request) {
	s.releaseMetadata(w, r, true)
}

func (s *Server) releaseMetadata(w http.ResponseWriter, r *http.Request, set bool) {
	p, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, nil, err)
		return
	}
	if p.Kind != protocol.PrincipalRunner {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeForbidden, "only runners download releases"))
		return
	}
	id := r.PathValue("upgrade")
	if !release.ValidID(id) || s.cfg.ReleaseDir == "" {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeNotFound, "release is unavailable"))
		return
	}
	batch, err := s.app.GetUpgrade(r.Context(), id)
	if err != nil {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeNotFound, "release is unavailable"))
		return
	}
	allowed := false
	for _, h := range batch.Hosts {
		if h.HostID == p.Subject {
			allowed = true
			break
		}
	}
	if !allowed {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeForbidden, "host is outside this upgrade"))
		return
	}
	bundle, err := release.Load(filepath.Join(s.cfg.ReleaseDir, id))
	if err != nil || bundle.ManifestSHA256 != batch.ManifestSHA256 || (batch.ReleaseSetSHA256 != "" && bundle.SetSHA256 != batch.ReleaseSetSHA256) {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidTransition, "staged release failed verification"))
		return
	}
	relative := "manifest.json"
	if set {
		if batch.ReleaseSetSHA256 == "" || bundle.Set == nil {
			s.writeError(w, r, nil, protocol.Errorf(protocol.CodeNotFound, "complete release set is unavailable"))
			return
		}
		relative = "release-set.json"
	}
	if sha := r.PathValue("sha"); sha != "" {
		relative = ""
		for _, a := range bundle.Artifacts() {
			if a.SHA256 == sha {
				relative = a.Path
				break
			}
		}
		if relative == "" {
			s.writeError(w, r, nil, protocol.Errorf(protocol.CodeNotFound, "artifact not in release"))
			return
		}
	}
	path, err := release.RegularPath(bundle.Dir, relative)
	if err != nil {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidTransition, "release file is unavailable"))
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.writeError(w, r, nil, protocol.Errorf(protocol.CodeInvalidTransition, "release file is unavailable"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, relative, time.Time{}, f)
}
