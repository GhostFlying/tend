package views

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (resolver *usageResolver) attribution(ctx context.Context, r protocol.TokenUsageRecord) protocol.UsageAttribution {
	q := resolver.q
	a := protocol.UsageAttribution{IssuePath: []protocol.UsageIssueRef{}, Categories: []string{}, Role: r.Role}
	conversation := strings.TrimPrefix(r.Subject, "conversation:")
	if r.RecordID != "" {
		conversation = ""
		a.Role = "unknown"
		var conflicted bool
		_ = q.QueryRowContext(ctx, `SELECT run_id,conversation_id,attribution_conflict FROM token_usage_bindings WHERE source=? AND request_id=?`, r.Source, r.RecordID).Scan(&a.RunID, &conversation, &conflicted)
		if conflicted {
			return a
		}
		if conversation == "main" {
			a.Role = "main"
		} else if conversation != "" {
			a.Role = "consultation"
		}
	}
	if strings.HasPrefix(r.Subject, "run:") {
		a.RunID = strings.TrimPrefix(r.Subject, "run:")
		conversation = ""
	}
	if a.RunID == "" {
		_ = q.QueryRowContext(ctx, `SELECT run_id FROM token_usage_bindings WHERE source=? AND request_id=? AND attribution_conflict=0`, r.Source, r.RecordID).Scan(&a.RunID)
	}
	if a.RunID == "" {
		_ = q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(e.private_snapshot,'$.runner_run_id'),'') FROM main_turns t JOIN hosted_executions e ON e.execution_id=t.request_id WHERE t.thread_id=? AND t.turn_id=? LIMIT 1`, r.ThreadID, r.TurnID).Scan(&a.RunID)
	}
	if a.RunID == "" {
		a.RunID = resolver.runForTurn(ctx, r.ThreadID, r.TurnID)
	}
	if a.RunID == "" {
		var snapshotRaw string
		if q.QueryRowContext(ctx, `SELECT h.private_snapshot,t.status FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE t.thread_id=? AND (t.turn_id=? OR t.turn_id=?) ORDER BY h.created_at DESC LIMIT 1`, r.ThreadID, r.TurnID, r.RootTurnID).Scan(&snapshotRaw, &a.ExecutionState) == nil {
			var snapshot struct {
				Identity   protocol.AgentIdentity `json:"identity"`
				Definition struct {
					Name       string   `json:"display_name"`
					Categories []string `json:"categories"`
				} `json:"definition"`
			}
			if json.Unmarshal([]byte(snapshotRaw), &snapshot) == nil {
				a.Agent = snapshot.Definition.Name
				a.Categories = snapshot.Definition.Categories
				if snapshot.Identity.LocalID != "" {
					a.AgentID = snapshot.Identity.ExecutionEnvironmentID + "/" + snapshot.Identity.LocalID
				}
			}
		}
	}
	issue := ""
	if a.RunID != "" {
		_ = q.QueryRowContext(ctx, `SELECT t.task_id,t.title,r.role,r.observed_state,COALESCE(json_extract(p.pin_json,'$.identity.execution_environment_id')||'/'||json_extract(p.pin_json,'$.identity.local_id'),'') FROM runs r JOIN tasks t USING(task_id) LEFT JOIN run_agent_executions p USING(run_id) WHERE r.run_id=?`, a.RunID).Scan(&a.TaskID, &a.TaskTitle, &a.Role, &a.ExecutionState, &a.AgentID)
		var pinRaw, projectionRaw string
		if q.QueryRowContext(ctx, `SELECT p.pin_json,v.projection_json FROM run_agent_executions p JOIN agent_inventory_versions v ON v.host_id=json_extract(p.pin_json,'$.identity.execution_environment_id') AND v.server_revision=json_extract(p.pin_json,'$.inventory_revision') WHERE p.run_id=?`, a.RunID).Scan(&pinRaw, &projectionRaw) == nil {
			var pin protocol.RunExecutionPin
			var inventory protocol.AgentInventoryProjection
			if json.Unmarshal([]byte(pinRaw), &pin) == nil && json.Unmarshal([]byte(projectionRaw), &inventory) == nil {
				a.AgentID = pin.Identity.ExecutionEnvironmentID + "/" + pin.Identity.LocalID
				for _, definition := range inventory.Agents {
					if definition.Identity == pin.Identity && definition.Revision == pin.AgentRevision {
						a.Agent = definition.DisplayName
						a.Categories = definition.Categories
						break
					}
				}
			}
		}
		_ = q.QueryRowContext(ctx, `SELECT work_item_id FROM task_work_item_owners WHERE task_id=?`, a.TaskID).Scan(&issue)
		if issue == "" {
			_ = q.QueryRowContext(ctx, `SELECT issue_id FROM run_issue_responsibilities WHERE run_id=?`, a.RunID).Scan(&issue)
		}
	}
	if a.Agent == "" && r.RecordID != "" {
		if actor, ok := resolver.actor(ctx, conversation, r.ThreadID, r.ParentThreadID, r.SessionID); ok {
			a.Agent, a.AgentID = actor.name, actor.id
			_ = json.Unmarshal([]byte(actor.categories), &a.Categories)
		}
	}
	if issue == "" && strings.HasPrefix(conversation, "issue:") {
		issue = strings.TrimPrefix(conversation, "issue:")
		a.Role = "issue_manager"
	}
	if a.RunID == "" && conversation != "" && conversation != "main" && issue == "" {
		a.Role = "consultation"
	}
	if r.Purpose == "internal_summary" {
		a.Role = "internal_summary"
	}
	seen := map[string]bool{}
	for issue != "" && !seen[issue] {
		seen[issue] = true
		var title, parent string
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(w.body_json,'$.title'),''),COALESCE(i.parent_issue_id,'') FROM work_items w JOIN issue_controls i ON i.issue_id=w.work_item_id WHERE w.work_item_id=?`, issue).Scan(&title, &parent); err != nil {
			break
		}
		a.IssuePath = append([]protocol.UsageIssueRef{{ID: issue, Title: title}}, a.IssuePath...)
		issue = parent
	}
	return a
}

func sessionID(r protocol.TokenUsageRecord) string {
	if r.SessionID != "" {
		return r.SessionID
	}
	// Do not invent a native Session by relabeling a thread or conversation.
	return "legacy:" + r.Subject
}

func addSession(groups map[string]*protocol.UsageSession, r protocol.TokenUsageRecord) {
	id := sessionID(r)
	s := groups[id]
	if s == nil {
		s = &protocol.UsageSession{ID: id, ParentSessionID: r.ParentSessionID, Models: []string{}, Attributions: []protocol.UsageAttribution{}}
		groups[id] = s
	}
	addUsageRecord(&s.Totals, r)
	if r.OccurredAt != nil {
		at := *r.OccurredAt
		if s.FirstAt == nil || at < *s.FirstAt {
			s.FirstAt = &at
		}
		if s.LastAt == nil || at > *s.LastAt {
			s.LastAt = &at
		}
	}
	{
		model := r.Model
		if model == "" {
			model = "实际模型未提供"
		}
		found := false
		for _, existing := range s.Models {
			found = found || existing == model
		}
		if !found {
			s.Models = append(s.Models, model)
			sort.Strings(s.Models)
		}
	}
	key, _ := json.Marshal(r.Attribution)
	found := false
	for _, a := range s.Attributions {
		b, _ := json.Marshal(a)
		found = found || string(key) == string(b)
	}
	if !found {
		s.Attributions = append(s.Attributions, r.Attribution)
		s.Executions = append(s.Executions, protocol.UsageExecution{Attribution: r.Attribution, Measurements: []protocol.UsageMeasurement{}})
	}
	for i := range s.Executions {
		b, _ := json.Marshal(s.Executions[i].Attribution)
		if string(b) == string(key) {
			execution := &s.Executions[i]
			addUsageRecord(&execution.Totals, r)
			measurement := -1
			for j, m := range execution.Measurements {
				if m.Model == r.Model && m.CLI == r.Provider && m.CLIVersion == r.NativeVersion {
					measurement = j
					break
				}
			}
			if measurement < 0 {
				execution.Measurements = append(execution.Measurements, protocol.UsageMeasurement{Model: r.Model, CLI: r.Provider, CLIVersion: r.NativeVersion})
				measurement = len(execution.Measurements) - 1
			}
			addUsageRecord(&execution.Measurements[measurement].Totals, r)
			break
		}
	}
}

func sessions(groups map[string]*protocol.UsageSession) []protocol.UsageSession {
	out := []protocol.UsageSession{}
	for _, s := range groups {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := int64(0), int64(0)
		if out[i].LastAt != nil {
			a = *out[i].LastAt
		}
		if out[j].LastAt != nil {
			b = *out[j].LastAt
		}
		if a == b {
			return out[i].ID < out[j].ID
		}
		return a > b
	})
	return out
}

type usageGrouping struct {
	sessions      map[string]*protocol.UsageSession
	issues        map[string]*protocol.UsageIssue
	issueSessions map[string]map[string]*protocol.UsageSession
}

func newUsageGrouping() *usageGrouping {
	return &usageGrouping{sessions: map[string]*protocol.UsageSession{}, issues: map[string]*protocol.UsageIssue{}, issueSessions: map[string]map[string]*protocol.UsageSession{}}
}
func (g *usageGrouping) add(r protocol.TokenUsageRecord) {
	addSession(g.sessions, r)
	path := r.Attribution.IssuePath
	if len(path) == 0 {
		path = []protocol.UsageIssueRef{{ID: "", Title: "主会话与未归属用量"}}
	}
	for i, ref := range path {
		issue := g.issues[ref.ID]
		if issue == nil {
			issue = &protocol.UsageIssue{ID: ref.ID, Title: ref.Title, Models: []string{}, Path: append([]protocol.UsageIssueRef{}, path[:i+1]...), Sessions: []protocol.UsageSession{}}
			if i > 0 {
				issue.ParentID = path[i-1].ID
			}
			g.issues[ref.ID] = issue
			g.issueSessions[ref.ID] = map[string]*protocol.UsageSession{}
		}
		model := r.Model
		if model == "" {
			model = "实际模型未提供"
		}
		found := false
		for _, m := range issue.Models {
			found = found || m == model
		}
		if !found {
			issue.Models = append(issue.Models, model)
			sort.Strings(issue.Models)
		}
		addUsageRecord(&issue.Totals, r)
		if r.OccurredAt != nil && (issue.LastAt == nil || *r.OccurredAt > *issue.LastAt) {
			at := *r.OccurredAt
			issue.LastAt = &at
		}
		if i == len(path)-1 {
			addUsageRecord(&issue.Direct, r)
			addSession(g.issueSessions[ref.ID], r)
		}
	}
}
func (g *usageGrouping) finish(v *protocol.TokenUsageView) {
	v.Sessions = sessions(g.sessions)
	v.Issues = []protocol.UsageIssue{}
	for id, issue := range g.issues {
		issue.Sessions = sessions(g.issueSessions[id])
		v.Issues = append(v.Issues, *issue)
	}
	sort.Slice(v.Issues, func(i, j int) bool {
		a, b := int64(0), int64(0)
		if v.Issues[i].LastAt != nil {
			a = *v.Issues[i].LastAt
		}
		if v.Issues[j].LastAt != nil {
			b = *v.Issues[j].LastAt
		}
		if a != b {
			return a > b
		}
		return v.Issues[i].ID < v.Issues[j].ID
	})
}
