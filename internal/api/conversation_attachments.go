package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	op, err := s.app.UploadConversationAttachment(r.Context(), p, r.URL.Query().Get("request_id"), r.URL.Query().Get("name"), r.URL.Query().Get("media_type"), r.Body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.APIResponse{Operation: &op})
}

func (s *Server) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	if _, err := s.filePrincipal(r, "attachment"); err != nil {
		s.writeError(w, r, err)
		return
	}
	f, stream, err := s.app.OpenConversationAttachment(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer stream.Close()
	media := "application/octet-stream"
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" {
		switch f.MediaType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
			media = f.MediaType
			disposition = "inline"
		}
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-SHA256", f.SHA256)
	http.ServeContent(w, r, f.Name, time.Time{}, stream)
}

type fileGrant struct {
	Login  string `json:"owner"`
	ID     string `json:"file"`
	Kind   string `json:"kind"`
	Inline bool   `json:"inline"`
	Issued int64  `json:"issued"`
}

const fileGrantTTL = 5 * time.Minute

func (s *Server) signFileGrant(g fileGrant) string {
	b, _ := json.Marshal(g)
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("tend/file-download/v1\x00"))
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// filePrincipal permits a scoped download grant only for this file and owner.
// Ordinary owner tokens continue to travel in headers, never in download URLs.
func (s *Server) filePrincipal(r *http.Request, kind string) (protocol.Principal, error) {
	if r.Header.Get("Authorization") != "" {
		return s.owner(r)
	}
	login, err := s.ownerLogin(r)
	if err != nil {
		return protocol.Principal{}, err
	}
	token := r.URL.Query().Get("download")
	body, mac, ok := strings.Cut(token, ".")
	if !ok {
		return protocol.Principal{}, errNoToken
	}
	b, e1 := base64.RawURLEncoding.DecodeString(body)
	sig, e2 := base64.RawURLEncoding.DecodeString(mac)
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("tend/file-download/v1\x00"))
	m.Write(b)
	var g fileGrant
	if e1 != nil || e2 != nil || !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(b, &g) != nil {
		return protocol.Principal{}, errNoToken
	}
	age := s.cfg.Now().Sub(time.UnixMilli(g.Issued))
	if g.Login != login || g.ID != r.PathValue("id") || g.Kind != kind || g.Inline != (r.URL.Query().Get("inline") == "1") || age < 0 || age > fileGrantTTL {
		return protocol.Principal{}, errNoToken
	}
	return protocol.Principal{Kind: protocol.PrincipalOwner, Subject: login, Client: "file-download"}, nil
}

func (s *Server) attachmentDownloadURL(w http.ResponseWriter, r *http.Request) {
	s.downloadURL(w, r, "attachment")
}
func (s *Server) deliverableDownloadURL(w http.ResponseWriter, r *http.Request) {
	s.downloadURL(w, r, "deliverable")
}
func (s *Server) downloadURL(w http.ResponseWriter, r *http.Request, kind string) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	prefix := "/api/v1/conversation/attachments/"
	if kind == "attachment" {
		_, err = s.app.ConversationAttachment(r.Context(), id)
	} else {
		_, err = s.app.DeliverableFile(r.Context(), p, id)
		prefix = "/api/v1/files/"
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	inline := kind == "attachment" && r.URL.Query().Get("inline") == "1"
	g := fileGrant{Login: p.Subject, ID: id, Kind: kind, Inline: inline, Issued: s.cfg.Now().UnixMilli()}
	query := url.Values{"download": {s.signFileGrant(g)}}
	if inline {
		query.Set("inline", strconv.Itoa(1))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, protocol.DownloadURLResponse{URL: prefix + url.PathEscape(id) + "?" + query.Encode()})
}
