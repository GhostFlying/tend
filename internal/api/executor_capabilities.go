package api

import (
	"net/http"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func capabilityLimit(r *http.Request) (int, error) {
	if !r.URL.Query().Has("limit") {
		return 50, nil
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > 100 {
		return 0, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100")
	}
	return n, nil
}

func rejectCapabilityQuery(r *http.Request, allowed ...string) error {
	for key, values := range r.URL.Query() {
		found := false
		for _, a := range allowed {
			if a == key {
				found = true
				break
			}
		}
		if !found || len(values) != 1 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "unknown or repeated capability query parameter")
		}
	}
	return nil
}

func (s *Server) executionCapabilities(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := rejectCapabilityQuery(r, "state", "search", "after", "limit"); err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := capabilityLimit(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	p, err := s.app.CapabilityDefinitions(r.Context(), r.PathValue("id"), q.Get("state"), q.Get("search"), q.Get("after"), limit)
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) executionCapability(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		if err := rejectCapabilityQuery(r, "revision"); err != nil {
			return nil, err
		}
		var revision int64
		if r.URL.Query().Has("revision") {
			var err error
			revision, err = strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
			if err != nil || revision < 1 {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "revision must be positive")
			}
		}
		return store.GetCapabilityDefinition(r.Context(), q, r.PathValue("id"), revision)
	})
}

func (s *Server) agentCapabilities(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := rejectCapabilityQuery(r, "environment", "agent", "runtime", "capability", "state", "category", "purpose", "after", "limit"); err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := capabilityLimit(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	f := protocol.AgentCapabilityQuery{Category: q.Get("category"), Purpose: q.Get("purpose"), Environment: q.Get("environment"), Agent: q.Get("agent"), Runtime: q.Get("runtime"), Capability: q.Get("capability"), State: q.Get("state"), After: q.Get("after"), Limit: limit}
	if r.PathValue("environment") != "" {
		if f.Environment != "" || f.Agent != "" || f.After != "" {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "show identity cannot be combined with environment, agent or after"))
			return
		}
		f.Environment, f.Agent = r.PathValue("environment"), r.PathValue("agent")
	}
	p, err := s.app.CapabilityAgents(r.Context(), f)
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	if r.PathValue("environment") != "" {
		if len(p.Items) != 1 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeNotFound, "agent capability projection not found"))
			return
		}
		writeJSON(w, http.StatusOK, p.Items[0])
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) capabilityEvidenceHistory(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := rejectCapabilityQuery(r, "capability", "kind", "state", "after", "limit"); err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := capabilityLimit(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	p, err := s.app.CapabilityEvidenceHistory(r.Context(), store.CapabilityEvidenceQuery{Host: r.PathValue("environment"), Agent: r.PathValue("agent"), Capability: q.Get("capability"), Kind: q.Get("kind"), State: q.Get("state")}, q.Get("after"), limit)
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) capabilityEvidence(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		if err := rejectCapabilityQuery(r); err != nil {
			return nil, err
		}
		return store.GetCapabilityEvidence(r.Context(), q, r.PathValue("id"))
	})
}

func (s *Server) taskCapabilityMatch(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := rejectCapabilityQuery(r, "role", "spec_version", "environment", "agent", "runtime", "capability", "state", "category", "purpose", "after", "limit"); err != nil {
		s.writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	version := int64(0)
	if q.Has("spec_version") {
		var err error
		version, err = strconv.ParseInt(q.Get("spec_version"), 10, 64)
		if err != nil || version < 1 {
			s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "spec_version must be positive"))
			return
		}
	}
	limit, err := capabilityLimit(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	p, err := s.app.TaskCapabilityExplanationPage(r.Context(), r.PathValue("id"), version, protocol.RunRole(q.Get("role")), protocol.AgentCapabilityQuery{Environment: q.Get("environment"), Agent: q.Get("agent"), Runtime: q.Get("runtime"), Capability: q.Get("capability"), State: q.Get("state"), Category: q.Get("category"), Purpose: q.Get("purpose"), After: q.Get("after"), Limit: limit})
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}
