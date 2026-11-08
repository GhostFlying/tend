package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (s *Server) stageExtension(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var in protocol.ExtensionStageRequest
	if err = decodeBodyLimit(w, r, &in, 90<<20); err != nil {
		s.writeError(w, r, err)
		return
	}
	op, err := s.app.StageExtension(r.Context(), p, in)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.APIResponse{Operation: &op})
}

func (s *Server) extensionRelease(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		release, _, err := store.GetExtensionRelease(r.Context(), q, r.PathValue("id"))
		return release, err
	})
}

func (s *Server) extensionView(w http.ResponseWriter, r *http.Request) {
	s.read(w, r, func(_ protocol.Principal, q store.Querier) (any, error) {
		state, err := store.GetExtensionState(r.Context(), q)
		if err != nil {
			return nil, err
		}
		query := r.URL.Query()
		limit := 100
		if raw := query.Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 200 {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be 1..200")
			}
		}
		after := query.Get("after")
		switch r.PathValue("view") {
		case "state":
			return state, nil
		case "releases":
			return store.ListExtensionReleases(r.Context(), q, after, limit)
		case "inventory":
			page := protocol.ExtensionResourcePage{Items: []protocol.ExtensionResource{}}
			for _, res := range state.Catalog.Resources {
				if query.Get("kind") != "" && res.Kind != query.Get("kind") || query.Get("id") != "" && res.ID != query.Get("id") || query.Get("version") != "" && res.Version != query.Get("version") {
					continue
				}
				cursor := res.Kind + ":" + res.ID + ":" + res.Version
				if cursor <= after {
					continue
				}
				if len(page.Items) == limit {
					page.More = true
					break
				}
				page.Next = cursor
				page.Items = append(page.Items, res)
			}
			if !page.More {
				page.Next = ""
			}
			return page, nil
		case "categories", "agents":
			bindings := state.Catalog.Categories
			if r.PathValue("view") == "agents" {
				bindings = state.Catalog.Agents
			}
			page := protocol.ExtensionBindingPage{Items: []protocol.ExtensionBinding{}}
			for _, b := range bindings {
				cursor := b.CategoryID
				if cursor == "" {
					cursor = b.Agent.ExecutionEnvironmentID + "/" + b.Agent.LocalID
				}
				if query.Get("id") != "" && cursor != query.Get("id") || cursor <= after {
					continue
				}
				if len(page.Items) == limit {
					page.More = true
					break
				}
				page.Next = cursor
				page.Items = append(page.Items, b)
			}
			if !page.More {
				page.Next = ""
			}
			return page, nil
		case "resolve":
			release := protocol.ExtensionRelease{CompositionVersion: protocol.ExtensionCompositionVersion, Catalog: state.Catalog}
			if id := query.Get("release"); id != "" {
				release, _, err = store.GetExtensionRelease(r.Context(), q, id)
				if err != nil {
					return nil, err
				}
			}
			combination, err := extensions.Resolve(release, query.Get("category"), protocol.AgentIdentity{ExecutionEnvironmentID: query.Get("environment"), LocalID: query.Get("agent")})
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "cannot resolve extensions: %s", err)
			}
			return combination, nil
		case "plan":
			release, raw, err := extensions.NewRelease(state.Catalog)
			if err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid draft: %s", err)
			}
			plan := protocol.ExtensionPlan{Draft: state.Catalog, Candidate: protocol.ExtensionReleaseRef{ID: release.ID, ManifestSHA256: extensions.Hash(raw), ManifestSize: int64(len(raw))}, Changed: true, Affected: []protocol.AgentIdentity{}}
			if state.Published != nil {
				old, _, err := store.GetExtensionRelease(r.Context(), q, state.Published.ID)
				if err != nil {
					return nil, err
				}
				plan.Published = &old
				plan.Changed = old.ID != release.ID
			}
			plan.Affected, err = app.ExtensionAffectedAgents(r.Context(), q, release, plan.Published)
			return plan, err
		case "status", "doctor":
			status := protocol.ExtensionStatus{Revision: state.Revision, Published: state.Published, Hosts: []protocol.ExtensionHostView{}}
			hosts, err := store.ListActiveHosts(r.Context(), q)
			if err != nil {
				return nil, err
			}
			for _, host := range hosts {
				if host.HostID <= after || query.Get("environment") != "" && host.HostID != query.Get("environment") {
					continue
				}
				row := protocol.ExtensionHostView{HostID: host.HostID, State: "unmigrated"}
				runtime, err := store.GetHostRuntime(r.Context(), q, host.HostID)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return nil, err
				}
				if err == nil {
					row.Online = runtime.Online
					row.Status = runtime.Runtime.Extensions
					if row.Status != nil && row.Status.Enabled {
						row.State = "managed"
					}
				}
				status.Hosts = append(status.Hosts, row)
			}
			if s.cfg.LocalExtensions != nil {
				id, local, err := s.cfg.LocalExtensions(r.Context())
				if err != nil {
					return nil, err
				}
				if id > after && (query.Get("environment") == "" || id == query.Get("environment")) {
					status.Hosts = append(status.Hosts, protocol.ExtensionHostView{HostID: id, State: "managed", Online: true, Status: &local})
				}
			}
			slices.SortFunc(status.Hosts, func(a, b protocol.ExtensionHostView) int { return strings.Compare(a.HostID, b.HostID) })
			if len(status.Hosts) > limit {
				status.Hosts = status.Hosts[:limit]
				status.More = true
				status.Next = status.Hosts[len(status.Hosts)-1].HostID
			}
			return status, nil
		default:
			return nil, protocol.Errorf(protocol.CodeNotFound, "extension view does not exist")
		}
	})
}
