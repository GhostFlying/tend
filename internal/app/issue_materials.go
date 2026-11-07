package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func materialIssueIDs(ctx context.Context, q store.Querier, issue string) ([]string, error) {
	var out []string
	for id := issue; id != ""; {
		if slices.Contains(out, id) {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "Issue ancestry contains a cycle")
		}
		out = append(out, id)
		current, err := store.GetCanonicalIssue(ctx, q, id)
		if errors.Is(err, store.ErrNotFound) {
			if _, err := store.GetIssue(ctx, q, id); err != nil {
				return nil, err
			}
			break
		}
		if err != nil {
			return nil, err
		}
		id = current.ParentIssueID
	}
	return out, nil
}

func runMaterialScope(ctx context.Context, q store.Querier, p protocol.Principal) (store.RunRow, store.MaterialScope, error) {
	var r store.RunRow
	var scope store.MaterialScope
	if p.Kind != protocol.PrincipalRun || p.Role != protocol.RoleImplement && p.Role != protocol.RoleAccept && p.Role != protocol.RoleIssueManager {
		return r, scope, protocol.Errorf(protocol.CodeForbidden, "Run identity required")
	}
	r, err := store.GetRun(ctx, q, p.Subject)
	if err != nil {
		return r, scope, err
	}
	if r.Role != p.Role {
		return r, scope, protocol.Errorf(protocol.CodeForbidden, "Run role mismatch")
	}
	lease, err := store.GetLeaseByRun(ctx, q, r.RunID)
	if err != nil {
		return r, scope, err
	}
	if lease.Status == store.LeaseReleased {
		return r, scope, protocol.Errorf(protocol.CodeForbidden, "Run credential is retired")
	}
	var primary string
	if p.Role == protocol.RoleIssueManager {
		manager, err := store.GetIssueManagerTask(ctx, q, r.TaskID)
		if err != nil {
			return r, scope, err
		}
		primary = manager.IssueID
	} else {
		primary, err = store.GetTaskIssueOwner(ctx, q, r.TaskID)
		if err != nil {
			return r, scope, err
		}
	}
	ids, err := materialIssueIDs(ctx, q, primary)
	if err != nil {
		return r, scope, err
	}
	if p.Role != protocol.RoleIssueManager {
		items, _, err := store.TaskIssueContextItems(ctx, q, r.TaskID)
		if err != nil {
			return r, scope, err
		}
		for _, item := range items {
			if !slices.Contains(ids, item.WorkItemID) {
				ids = append(ids, item.WorkItemID)
			}
		}
		action, err := store.GetAction(ctx, q, r.ActionID)
		if err != nil {
			return r, scope, err
		}
		raw, err := store.GetSpec(ctx, q, r.TaskID, action.TargetSpecVersion)
		if err != nil {
			return r, scope, err
		}
		var spec struct {
			SharedContext []protocol.TaskWorkContext `json:"shared_context"`
		}
		if err = json.Unmarshal([]byte(raw), &spec); err != nil {
			return r, scope, err
		}
		for _, item := range spec.SharedContext {
			if !slices.Contains(ids, item.WorkItemID) {
				ids = append(ids, item.WorkItemID)
			}
		}
	}
	responsibility, err := store.RunIssueResponsibility(ctx, q, r.RunID)
	if err == nil && !slices.Contains(ids, responsibility.IssueID) {
		ids = append(ids, responsibility.IssueID)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return r, scope, err
	}
	scope, err = views.MaterialScope(ctx, q, ids)
	if err != nil {
		return r, scope, err
	}
	return r, scope, err
}

func (a *App) ReadRunContext(ctx context.Context, p protocol.Principal, query protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
	var result protocol.RunContext
	var scope store.MaterialScope
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, s, err := runMaterialScope(ctx, tx.Q(), p)
		if err != nil {
			return err
		}
		result.RunID, result.TaskID, result.Role = r.RunID, r.TaskID, r.Role
		action, err := store.GetAction(ctx, tx.Q(), r.ActionID)
		if err != nil {
			return err
		}
		result.SpecVersion = action.TargetSpecVersion
		scope = s
		return nil
	})
	if err != nil {
		return result, err
	}
	query, err = protocol.NormalizeIssueMaterialsQuery(query)
	if err != nil {
		return result, err
	}
	if query.Kind == "task_capabilities" && query.ID == "" {
		query.ID = result.TaskID
	}
	result.Materials, err = a.readMaterials(ctx, scope, query, result.Role)
	return result, err
}
func (a *App) ReadRunContextFile(ctx context.Context, p protocol.Principal, id string) (protocol.IssueMaterialFile, error) {
	var file protocol.IssueMaterialFile
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		_, scope, err := runMaterialScope(ctx, tx.Q(), p)
		if err != nil {
			return err
		}
		file, err = views.IssueMaterialFile(ctx, tx.Q(), scope, id)
		return err
	})
	return file, err
}
func (a *App) OpenRunContextFile(ctx context.Context, p protocol.Principal, id string) (protocol.IssueMaterialFile, *os.File, error) {
	file, err := a.ReadRunContextFile(ctx, p, id)
	if err != nil {
		return file, nil, err
	}
	body, err := a.store.FileStore().Open(file.SHA256)
	return file, body, err
}
func (a *App) ReadIssueMaterials(ctx context.Context, issueID string, query protocol.IssueMaterialsQuery) (protocol.IssueMaterials, error) {
	return a.ReadScopedIssueMaterials(ctx, "", issueID, query)
}
func (a *App) ReadScopedIssueMaterials(ctx context.Context, rootIssueID, issueID string, query protocol.IssueMaterialsQuery) (protocol.IssueMaterials, error) {
	var scope store.MaterialScope
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := materialIssueIDs(ctx, tx.Q(), issueID)
		if err != nil {
			return errNotFound(err, "Issue does not exist")
		}
		if rootIssueID != "" && !slices.Contains(ids, rootIssueID) {
			return protocol.Errorf(protocol.CodeForbidden, "Issue is outside this responsibility")
		}
		scope, err = views.MaterialScope(ctx, tx.Q(), ids)
		return err
	})
	if err != nil {
		return protocol.IssueMaterials{}, err
	}
	query, err = protocol.NormalizeIssueMaterialsQuery(query)
	if err != nil {
		return protocol.IssueMaterials{}, err
	}
	return a.readMaterials(ctx, scope, query, protocol.RoleImplement)
}
func (a *App) ReadIssueMaterialFile(ctx context.Context, issueID, id string) (protocol.IssueMaterialFile, error) {
	var file protocol.IssueMaterialFile
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := materialIssueIDs(ctx, tx.Q(), issueID)
		if err != nil {
			return err
		}
		scope, err := views.MaterialScope(ctx, tx.Q(), ids)
		if err != nil {
			return err
		}
		file, err = views.IssueMaterialFile(ctx, tx.Q(), scope, id)
		return err
	})
	return file, err
}
func (a *App) OpenIssueMaterialFile(ctx context.Context, issueID, id string) (protocol.IssueMaterialFile, *os.File, error) {
	file, err := a.ReadIssueMaterialFile(ctx, issueID, id)
	if err != nil {
		return file, nil, err
	}
	body, err := a.store.FileStore().Open(file.SHA256)
	return file, body, err
}

func (a *App) readMaterials(ctx context.Context, scope store.MaterialScope, query protocol.IssueMaterialsQuery, role protocol.RunRole) (protocol.IssueMaterials, error) {
	var result protocol.IssueMaterials
	var err error
	if slices.Contains([]string{"capabilities", "agent_capabilities", "agent_capability_history", "task_capabilities"}, query.Kind) {
		result = protocol.IssueMaterials{Kind: query.Kind, IssueIDs: scope.IssueIDs}
		switch query.Kind {
		case "capabilities":
			page, e := a.CapabilityDefinitions(ctx, query.ID, "all", "", query.After, query.Limit)
			result.Capabilities = &page
			result.Next = page.Next
			err = e
		case "agent_capabilities", "agent_capability_history":
			environment, agent := "", ""
			if query.ID != "" {
				parts := strings.Split(query.ID, "/")
				if len(parts) != 2 {
					return result, protocol.Errorf(protocol.CodeInvalidRequest, "agent identity is environment/agent")
				}
				environment, agent = parts[0], parts[1]
			}
			if query.Kind == "agent_capabilities" {
				page, e := a.CapabilityAgents(ctx, protocol.AgentCapabilityQuery{Environment: environment, Agent: agent, After: query.After, Limit: query.Limit})
				result.AgentCapabilities = &page
				result.Next = page.Next
				err = e
			} else {
				page, e := a.CapabilityAgentRevisions(ctx, environment, agent, 0, query.After, query.Limit)
				result.AgentCapabilityHistory = &page
				result.Next = page.Next
				err = e
			}
		case "task_capabilities":
			if query.ID == "" {
				return result, protocol.Errorf(protocol.CodeInvalidRequest, "task capability context requires a task ID")
			}
			err = a.store.Read(ctx, func(tx *store.ReadTx) error {
				ids, _, _, err := store.MaterialIDs(ctx, tx.Q(), scope, "tasks", query.ID, "", 1)
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					return protocol.Errorf(protocol.CodeForbidden, "Task is outside this Issue context")
				}
				return nil
			})
			if err != nil {
				return result, err
			}
			if role != protocol.RoleAccept {
				role = protocol.RoleImplement
			}
			page, e := a.TaskCapabilityExplanationPage(ctx, query.ID, 0, role, protocol.AgentCapabilityQuery{After: query.After, Limit: query.Limit})
			result.TaskCapabilities = &page
			result.Next = page.Next
			err = e
		}
		result.More = result.Next != ""
	} else {
		err = a.store.Read(ctx, func(tx *store.ReadTx) error {
			var e error
			result, e = views.IssueMaterials(ctx, tx.Q(), scope, query)
			return e
		})
	}
	if err != nil {
		return result, err
	}
	if role == protocol.RoleAccept {
		for i := range result.Tasks {
			var spec map[string]json.RawMessage
			if err := json.Unmarshal(result.Tasks[i].Spec, &spec); err != nil {
				return result, err
			}
			// Only this explicitly implementation-private field is withheld.
			// Submitted public artifacts remain ordinary readable materials.
			delete(spec, "implementation_context")
			result.Tasks[i].Spec, err = json.Marshal(spec)
			if err != nil {
				return result, err
			}
		}
	}
	if query.Kind == "deployment" {
		deployment, e := a.ReadIssueDeploymentPreparation(ctx, scope.IssueIDs[0])
		if e != nil {
			return result, e
		}
		result.Deployment = &deployment
	}
	if result.Seq == 0 {
		err = a.store.Read(ctx, func(tx *store.ReadTx) error { var e error; result.Seq, e = store.LatestEventSeq(ctx, tx.Q()); return e })
	}
	result.ObservedAt = a.now().UnixMilli()
	return result, err
}
