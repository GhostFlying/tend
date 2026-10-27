package api

import (
	"errors"
	"net/http"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (s *Server) agentConversations(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		v, err := store.AgentConversations(r.Context(), q, r.URL.Query().Get("search"), r.URL.Query().Get("status"), r.URL.Query().Get("after"), 30)
		v.RuntimeAvailable = s.cfg.HostedMainAgent
		return v, err
	})
}
func (s *Server) agentConversation(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return store.AgentConversation(r.Context(), q, r.PathValue("id"))
	})
}
func (s *Server) messageAssociations(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		return store.MessageAssociations(r.Context(), q, r.PathValue("id"))
	})
}
func (s *Server) workItemContext(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		after, err := intParam(r.URL.Query().Get("after"))
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid cursor")
		}
		return views.WorkItemContext(r.Context(), q, r.PathValue("id"), after)
	})
}
func (s *Server) agentRollout(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		if _, err := store.AgentConversation(r.Context(), q, r.PathValue("id")); err != nil {
			return nil, err
		}
		after, err := intParam(r.URL.Query().Get("after"))
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid cursor")
		}
		return store.AgentRollout(r.Context(), q, r.PathValue("id"), r.URL.Query().Get("turn_id"), after)
	})
}
func (s *Server) agentUsage(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		if _, err := store.AgentConversation(r.Context(), q, r.PathValue("id")); err != nil {
			return nil, err
		}
		v, err := store.ContextUsage(r.Context(), q, r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			return protocol.AgentContextUsage{Measurement: "pending"}, nil
		}
		return v, err
	})
}
func (s *Server) agentCompactions(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		before, err := intParam(r.URL.Query().Get("before"))
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid cursor")
		}
		return store.Compactions(r.Context(), q, r.PathValue("id"), before)
	})
}

func (s *Server) agentRolloutWindow(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		seq, err := intParam(r.PathValue("seq"))
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid rollout record")
		}
		offset, err := intParam(r.URL.Query().Get("offset"))
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid rollout offset")
		}
		return store.AgentRolloutWindow(r.Context(), q, r.PathValue("id"), seq, offset)
	})
}
