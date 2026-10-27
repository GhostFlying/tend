package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

func (e *env) retiredAgentToken(client string) string {
	return e.server.sign(token{Login: ownerLogin, ClientID: client, Scope: protocol.ScopeAgent, Issued: e.clock.Now().UnixMilli()})
}

func TestExternalCoordinatorRetired(t *testing.T) {
	e := newEnv(t)
	old := e.retiredAgentToken(clientID)
	if st := e.do("POST", "/api/v1/session", "", protocol.SessionRequest{ClientID: clientID, Scope: protocol.ScopeAgent}, nil); st != http.StatusBadRequest {
		t.Fatal(st)
	}
	if st := e.do("GET", "/api/v1/session", old, nil, nil); st != http.StatusUnauthorized {
		t.Fatal(st)
	}
	before := e.count("SELECT count(*) FROM request_records")
	for _, tok := range []string{old, e.login(protocol.ScopeOwner)} {
		for _, route := range []struct{ method, path string }{{"POST", "/api/v1/agent/hook"}, {"GET", "/api/v1/agent/tools"}, {"POST", "/api/v1/agent/tools/tasks_create"}} {
			st := e.do(route.method, route.path, tok, map[string]any{}, nil)
			if st != http.StatusNotFound {
				t.Errorf("%s %s = %d", route.method, route.path, st)
			}
		}
	}
	if after := e.count("SELECT count(*) FROM request_records"); after != before {
		t.Fatalf("retired endpoints wrote %d requests", after-before)
	}
}

// hostedSession drives the same in-process tool surface as a managed harness.
type hostedSession struct {
	e                   *env
	id, project, source string
	watermark           int64
}

func (e *env) hostedSession(id, project string) *hostedSession {
	s := &hostedSession{e: e, id: id, project: project, source: e.ids.New(protocol.PrefixMessage)}
	if err := e.store.Write(e.t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(e.t.Context(), store.MessageRow{MessageID: s.source, Author: "owner", Body: "Please perform the requested work"})
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return s
}
func (s *hostedSession) call(name string, args map[string]any, requestID string) toolAnswer {
	if requestID == "" {
		requestID = s.e.ids.New(protocol.PrefixRequest)
	}
	if name == "tasks_create" {
		args["commit_seq"] = 1
		if s.project != "" {
			args["project"] = s.project
		}
	}
	raw, _ := json.Marshal(args)
	result := s.e.tools.Handle(s.e.t.Context(), tools.Call{Principal: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "hosted", Thread: s.id}, SourceMessageID: s.source, RequestID: requestID, Tool: name, Arguments: raw})
	var a toolAnswer
	if err := json.Unmarshal([]byte(result.Text), &a); err != nil {
		s.e.t.Fatal(err)
	}
	return a
}
func (s *hostedSession) read(full bool) string {
	var c contextbuilder.Composed
	err := s.e.store.Read(s.e.t.Context(), func(tx *store.ReadTx) error {
		var err error
		c, err = s.e.context.Compose(s.e.t.Context(), tx.Q(), contextbuilder.ComposeRequest{Full: full, Watermark: s.watermark})
		return err
	})
	if err != nil {
		s.e.t.Fatal(err)
	}
	s.watermark = c.ToSeq
	return c.Text
}
