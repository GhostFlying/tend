package api

import (
	"encoding/json"
	"net/http"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/tools"
)

// maxSessionID bounds an agent session ID.
const maxSessionID = 200

// agentToken authenticates a request that only an agent-scoped token may
// make: the hooks and the MCP bridge of an agent the owner works in.
func (s *Server) agentToken(r *http.Request) (token, error) {
	v, err := s.authenticate(r)
	if err != nil {
		return token{}, err
	}
	if v.Scope != protocol.ScopeAgent {
		return token{}, protocol.Errorf(protocol.CodeForbidden, "this request needs an agent token, not an %s token", v.Scope)
	}
	return v, nil
}

// agentPrincipal is the principal of an agent the owner works in: the
// token's client, the kind of agent and its session when known (D49).
func agentPrincipal(v token, agent protocol.AgentKind, sessionID string) (protocol.Principal, error) {
	if !agent.Valid() {
		return protocol.Principal{}, protocol.Errorf(protocol.CodeInvalidRequest, "agent must be one of %v", protocol.Agents)
	}
	if len(sessionID) > maxSessionID {
		return protocol.Principal{}, protocol.Errorf(protocol.CodeInvalidRequest, "session_id is longer than %d bytes", maxSessionID)
	}
	return protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: v.ClientID, Agent: agent, Thread: sessionID}, nil
}

// hook answers an agent hook with the context to add, if any (D47).
func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	v, err := s.agentToken(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req protocol.HookRequest
	if err := decodeBody(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	p, err := agentPrincipal(v, req.Agent, req.SessionID)
	if err == nil && req.SessionID == "" {
		err = protocol.Errorf(protocol.CodeInvalidRequest, "session_id is required")
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// The prompt is not part of the request: it is never stored.
	payload, err := json.Marshal(map[string]any{"event": req.Event, "source": req.Source})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	env := protocol.Envelope{
		RequestID: req.RequestID, Operation: "agent.hook",
		Target: protocol.Target{Type: "agent_session", ID: app.SessionKey(req.Agent, req.SessionID)}, Payload: payload,
	}
	op, err := s.app.Execute(r.Context(), p, &env, app.AgentHook{Compose: s.context.Compose})
	if err == nil && op.Status == protocol.OpFailed {
		err = op.Error
	}
	if err != nil {
		s.writeError(w, r, err, "request_id", req.RequestID, "agent", req.Agent, "event", req.Event)
		return
	}
	var res protocol.HookResponse
	if err := json.Unmarshal(op.Result, &res); err != nil {
		s.writeError(w, r, err, "request_id", req.RequestID)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// listTools describes the tools for agents the owner works in.
func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	if _, err := s.agentToken(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tools.AddonSpecs())
}

// callTool runs one tool call from the MCP bridge. Writes run under the
// bridge's request ID, which it keeps when it retries (D48).
func (s *Server) callTool(w http.ResponseWriter, r *http.Request) {
	v, err := s.agentToken(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req protocol.ToolRequest
	if err := decodeBody(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	p, err := agentPrincipal(v, req.Agent, req.SessionID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	res := s.tools.Handle(r.Context(), tools.Call{Principal: p, RequestID: req.RequestID, Tool: r.PathValue("name"), Arguments: req.Arguments})
	writeJSON(w, http.StatusOK, protocol.ToolResponse{Success: res.Success, Content: res.Text})
}
