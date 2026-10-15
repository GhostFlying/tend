package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Owner sessions (D42). Every request must come from a tailnet user listed
// as an owner. A session cookie binds the browser to a client ID; it is
// signed with a key derived from the server secret and is only valid
// together with the same owner's tailnet identity, so a copied cookie is
// useless from another user's device. Writes must repeat the client ID in
// a header, which cross-site requests cannot set.

const (
	cookieName = "tend_session"
	// HeaderClient carries the session's client ID on writes.
	HeaderClient = "X-Tend-Client"
)

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// session is the content of the cookie.
type session struct {
	Login    string `json:"l"`
	ClientID string `json:"c"`
	Issued   int64  `json:"i"`
}

var (
	errNoSession = protocol.Errorf(protocol.CodeUnauthenticated, "no valid session; create one")
	errNotOwner  = protocol.Errorf(protocol.CodeForbidden, "only owners may use this service")
)

func sessionKey(secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("tend/session/v1"))
	return m.Sum(nil)
}

func (s *Server) sign(v session) string {
	body, _ := json.Marshal(v)
	m := hmac.New(sha256.New, s.key)
	m.Write(body)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(m.Sum(nil))
}

// verify returns the session in a cookie value if its signature holds and
// it has not expired.
func (s *Server) verify(value string) (session, bool) {
	body64, mac64, ok := strings.Cut(value, ".")
	if !ok {
		return session{}, false
	}
	enc := base64.RawURLEncoding
	body, err1 := enc.DecodeString(body64)
	mac, err2 := enc.DecodeString(mac64)
	if err1 != nil || err2 != nil {
		return session{}, false
	}
	m := hmac.New(sha256.New, s.key)
	m.Write(body)
	if !hmac.Equal(mac, m.Sum(nil)) {
		return session{}, false
	}
	var v session
	if json.Unmarshal(body, &v) != nil {
		return session{}, false
	}
	issued := time.UnixMilli(v.Issued)
	if now := s.cfg.Now(); now.Before(issued) || now.Sub(issued) > s.cfg.SessionTTL {
		return session{}, false
	}
	return v, true
}

// ownerLogin returns the caller's tailnet login if it is an owner.
func (s *Server) ownerLogin(r *http.Request) (string, error) {
	id, err := s.whois.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil || id.User == "" || !slices.Contains(s.cfg.Owners, id.User) {
		s.log.WarnContext(r.Context(), "owner request from a non-owner", "remote", r.RemoteAddr, "node", id.Node, "user", id.User)
		return "", errNotOwner
	}
	return id.User, nil
}

// owner authenticates a request: an owner's tailnet identity with a
// session of the same owner, and for writes the session's client ID in
// HeaderClient.
func (s *Server) owner(r *http.Request, write bool) (protocol.Principal, error) {
	login, err := s.ownerLogin(r)
	if err != nil {
		return protocol.Principal{}, err
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return protocol.Principal{}, errNoSession
	}
	v, ok := s.verify(c.Value)
	if !ok || v.Login != login {
		return protocol.Principal{}, errNoSession
	}
	if write && r.Header.Get(HeaderClient) != v.ClientID {
		return protocol.Principal{}, protocol.Errorf(protocol.CodeForbidden, "writes must carry the session's client ID in %s", HeaderClient)
	}
	return protocol.Principal{Kind: protocol.PrincipalOwner, Subject: login, Client: v.ClientID}, nil
}

// createSession issues a session cookie to an owner for a client ID the
// client generated and keeps.
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	login, err := s.ownerLogin(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req protocol.SessionRequest
	if err := decodeBody(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if !clientIDPattern.MatchString(req.ClientID) {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "client_id must be 8 to 64 letters, digits, '-' or '_'"))
		return
	}
	// A cross-site form could otherwise replace the session with a client
	// ID of the attacker's choosing; it cannot set a header.
	if r.Header.Get(HeaderClient) != req.ClientID {
		s.writeError(w, r, protocol.Errorf(protocol.CodeForbidden, "creating a session must repeat client_id in %s", HeaderClient))
		return
	}
	v := session{Login: login, ClientID: req.ClientID, Issued: s.cfg.Now().UnixMilli()}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.sign(v), Path: "/", MaxAge: int(s.cfg.SessionTTL / time.Second),
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, protocol.SessionView{Login: login, ClientID: req.ClientID})
}

// readSession tells a client whether its session is still valid.
func (s *Server) readSession(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r, false)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.SessionView{Login: p.Subject, ClientID: p.Client})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "body is too large")
		}
		return protocol.Errorf(protocol.CodeInvalidRequest, "body does not match the request: %v", err)
	}
	return nil
}
