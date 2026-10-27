package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Owner tokens (D49). Every request must come from a tailnet user listed
// as an owner. A token binds a client ID and a scope to that owner; it is
// signed with a key derived from the server secret and is only valid
// together with the same owner's tailnet identity, so a copied token is
// useless from another user's device. Tokens travel in the Authorization
// header, which a cross-site request cannot set, so nothing ambient
// authorizes a request.

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// token is the signed content of a bearer token.
type token struct {
	Login    string              `json:"l"`
	ClientID string              `json:"c"`
	Scope    protocol.TokenScope `json:"s"`
	Issued   int64               `json:"i"`
}

var (
	errNoToken  = protocol.Errorf(protocol.CodeUnauthenticated, "no valid token; create one with POST /api/v1/session")
	errNotOwner = protocol.Errorf(protocol.CodeForbidden, "only owners may use this service")
)

// tokenKey derives the signing key. The label differs from the run
// credentials' (D29) so neither can stand in for the other.
func tokenKey(secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("tend/owner-token/v1"))
	return m.Sum(nil)
}

func (s *Server) sign(v token) string {
	body, _ := json.Marshal(v)
	m := hmac.New(sha256.New, s.key)
	m.Write(body)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(body) + "." + enc.EncodeToString(m.Sum(nil))
}

// verify returns the content of a token if its signature holds, its scope
// is known and it has not expired.
func (s *Server) verify(value string) (token, bool) {
	body64, mac64, ok := strings.Cut(value, ".")
	if !ok {
		return token{}, false
	}
	enc := base64.RawURLEncoding
	body, err1 := enc.DecodeString(body64)
	mac, err2 := enc.DecodeString(mac64)
	if err1 != nil || err2 != nil {
		return token{}, false
	}
	m := hmac.New(sha256.New, s.key)
	m.Write(body)
	if !hmac.Equal(mac, m.Sum(nil)) {
		return token{}, false
	}
	var v token
	if json.Unmarshal(body, &v) != nil || !v.Scope.Valid() {
		return token{}, false
	}
	issued := time.UnixMilli(v.Issued)
	if now := s.cfg.Now(); now.Before(issued) || now.Sub(issued) > s.cfg.TokenTTL {
		return token{}, false
	}
	return v, true
}

func (s *Server) expires(v token) time.Time {
	return time.UnixMilli(v.Issued).Add(s.cfg.TokenTTL).UTC()
}

// ownerLogin returns the owner the caller's device belongs to: its
// tailnet login if that is an owner, or the owner a configured node
// belongs to.
func (s *Server) ownerLogin(r *http.Request) (string, error) {
	id, err := s.whois.WhoIs(r.Context(), r.RemoteAddr)
	if err == nil && id.User != "" && slices.Contains(s.cfg.Owners, id.User) {
		return id.User, nil
	}
	if err == nil {
		for _, node := range slices.Sorted(maps.Keys(s.cfg.OwnerNodes)) {
			if login := s.cfg.OwnerNodes[node]; id.Matches([]string{node}) && slices.Contains(s.cfg.Owners, login) {
				return login, nil
			}
		}
	}
	s.log.WarnContext(r.Context(), "owner request from a non-owner", "remote", r.RemoteAddr, "node", id.Node, "user", id.User)
	return "", errNotOwner
}

// authenticate checks a request's tailnet identity and bearer token and
// returns the token's content. The token must belong to the same owner.
func (s *Server) authenticate(r *http.Request) (token, error) {
	login, err := s.ownerLogin(r)
	if err != nil {
		return token{}, err
	}
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return token{}, errNoToken
	}
	v, ok := s.verify(strings.TrimSpace(value))
	if !ok || v.Login != login {
		return token{}, errNoToken
	}
	return v, nil
}

// owner authenticates a request that only an owner-scoped token may make.
func (s *Server) owner(r *http.Request) (protocol.Principal, error) {
	v, err := s.authenticate(r)
	if err != nil {
		return protocol.Principal{}, err
	}
	if v.Scope != protocol.ScopeOwner {
		return protocol.Principal{}, protocol.Errorf(protocol.CodeForbidden, "this request needs an owner token, not an %s token", v.Scope)
	}
	return protocol.Principal{Kind: protocol.PrincipalOwner, Subject: v.Login, Client: v.ClientID}, nil
}

// createSession issues a token to an owner for a client ID the client
// generated and keeps.
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
	if !req.Scope.Valid() {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "scope must be %q; external agent access has been retired", protocol.ScopeOwner))
		return
	}
	v := token{Login: login, ClientID: req.ClientID, Scope: req.Scope, Issued: s.cfg.Now().UnixMilli()}
	writeJSON(w, http.StatusOK, protocol.SessionView{DeploymentID: w.Header().Get(deploymentHeader),
		Login: login, ClientID: v.ClientID, Scope: v.Scope, ExpiresAt: s.expires(v), Token: s.sign(v),
	})
}

// readSession tells a client whether its token is still valid. Tokens of
// either scope may ask.
func (s *Server) readSession(w http.ResponseWriter, r *http.Request) {
	v, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.SessionView{DeploymentID: w.Header().Get(deploymentHeader), Login: v.Login, ClientID: v.ClientID, Scope: v.Scope, ExpiresAt: s.expires(v)})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeBodyLimit(w, r, v, maxBody)
}

func decodeBodyLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
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
