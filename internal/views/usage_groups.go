package views

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (resolver *usageResolver) attribution(ctx context.Context, r protocol.TokenUsageRecord, bindingRun, bindingConversation string, bindingConflict bool) (protocol.UsageAttribution, error) {
	q := resolver.q
	a := protocol.UsageAttribution{IssuePath: []protocol.UsageIssueRef{}, Categories: []string{}, Role: r.Role}
	conversation := strings.TrimPrefix(r.Subject, "conversation:")
	if r.RecordID != "" {
		// The projection query already read this binding in the same snapshot.
		a.RunID, conversation, a.Role = bindingRun, bindingConversation, "unknown"
		if bindingConflict {
			return a, nil
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
		a.RunID = bindingRun
	}
	if a.RunID == "" {
		if _, err := scanUsageRow(q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(e.private_snapshot,'$.runner_run_id'),'') FROM main_turns t JOIN hosted_executions e ON e.execution_id=t.request_id WHERE t.thread_id=? AND t.turn_id=? LIMIT 1`, r.ThreadID, r.TurnID), &a.RunID); err != nil {
			return a, err
		}
	}
	if a.RunID == "" {
		var err error
		a.RunID, err = resolver.runForTurn(ctx, r.ThreadID, r.TurnID)
		if err != nil {
			return a, err
		}
	}
	if a.RunID == "" {
		var snapshotRaw string
		found, err := scanUsageRow(q.QueryRowContext(ctx, `SELECT h.private_snapshot,t.status FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE t.thread_id=? AND (t.turn_id=? OR t.turn_id=?) ORDER BY h.created_at DESC LIMIT 1`, r.ThreadID, r.TurnID, r.RootTurnID), &snapshotRaw, &a.ExecutionState)
		if err != nil {
			return a, err
		}
		if found {
			var snapshot struct {
				Identity   protocol.AgentIdentity `json:"identity"`
				Definition struct {
					Name       string   `json:"display_name"`
					Categories []string `json:"categories"`
				} `json:"definition"`
			}
			if err := json.Unmarshal([]byte(snapshotRaw), &snapshot); err != nil {
				return a, err
			}
			a.Agent, a.Categories = snapshot.Definition.Name, snapshot.Definition.Categories
			if snapshot.Identity.LocalID != "" {
				a.AgentID = snapshot.Identity.ExecutionEnvironmentID + "/" + snapshot.Identity.LocalID
			}
		}
	}
	issue := ""
	if a.RunID != "" {
		if _, err := scanUsageRow(q.QueryRowContext(ctx, `SELECT t.task_id,t.title,r.role,r.observed_state,COALESCE(json_extract(p.pin_json,'$.identity.execution_environment_id')||'/'||json_extract(p.pin_json,'$.identity.local_id'),'') FROM runs r JOIN tasks t USING(task_id) LEFT JOIN run_agent_executions p USING(run_id) WHERE r.run_id=?`, a.RunID), &a.TaskID, &a.TaskTitle, &a.Role, &a.ExecutionState, &a.AgentID); err != nil {
			return a, err
		}
		var pinRaw, projectionRaw string
		found, err := scanUsageRow(q.QueryRowContext(ctx, `SELECT p.pin_json,v.projection_json FROM run_agent_executions p JOIN agent_inventory_versions v ON v.host_id=json_extract(p.pin_json,'$.identity.execution_environment_id') AND v.server_revision=json_extract(p.pin_json,'$.inventory_revision') WHERE p.run_id=?`, a.RunID), &pinRaw, &projectionRaw)
		if err != nil {
			return a, err
		}
		if found {
			var pin protocol.RunExecutionPin
			var inventory protocol.AgentInventoryProjection
			if err := json.Unmarshal([]byte(pinRaw), &pin); err != nil {
				return a, err
			}
			if err := json.Unmarshal([]byte(projectionRaw), &inventory); err != nil {
				return a, err
			}
			a.AgentID = pin.Identity.ExecutionEnvironmentID + "/" + pin.Identity.LocalID
			for _, definition := range inventory.Agents {
				if definition.Identity == pin.Identity && definition.Revision == pin.AgentRevision {
					a.Agent, a.Categories = definition.DisplayName, definition.Categories
					break
				}
			}
		}
		if _, err := scanUsageRow(q.QueryRowContext(ctx, `SELECT work_item_id FROM task_work_item_owners WHERE task_id=?`, a.TaskID), &issue); err != nil {
			return a, err
		}
		if issue == "" {
			if _, err := scanUsageRow(q.QueryRowContext(ctx, `SELECT issue_id FROM run_issue_responsibilities WHERE run_id=?`, a.RunID), &issue); err != nil {
				return a, err
			}
		}
	}
	if a.Agent == "" && r.RecordID != "" {
		actor, found, err := resolver.actor(ctx, conversation, r.ThreadID, r.ParentThreadID, r.SessionID)
		if err != nil {
			return a, err
		}
		if found {
			a.Agent, a.AgentID = actor.name, actor.id
			if err := json.Unmarshal([]byte(actor.categories), &a.Categories); err != nil {
				return a, err
			}
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
		found, err := scanUsageRow(q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(w.body_json,'$.title'),''),COALESCE(i.parent_issue_id,'') FROM work_items w JOIN issue_controls i ON i.issue_id=w.work_item_id WHERE w.work_item_id=?`, issue), &title, &parent)
		if err != nil {
			return a, err
		}
		if !found {
			break
		}
		a.IssuePath = append([]protocol.UsageIssueRef{{ID: issue, Title: title}}, a.IssuePath...)
		issue = parent
	}
	return a, nil
}

func sessionID(r protocol.TokenUsageRecord) string {
	if r.SessionID != "" {
		return r.SessionID
	}
	// Do not invent a native Session by relabeling a thread or conversation.
	return "legacy:" + r.Subject
}

func addSession(groups map[string]*protocol.UsageSession, a UsageAggregate) {
	r := a.Record
	id := sessionID(r)
	s := groups[id]
	if s == nil {
		s = &protocol.UsageSession{ID: id, ParentSessionID: r.ParentSessionID, Models: []string{}, Attributions: []protocol.UsageAttribution{}}
		groups[id] = s
	}
	MergeUsageTotals(&s.Totals, a.Totals)
	if a.FirstAt != nil && (s.FirstAt == nil || *a.FirstAt < *s.FirstAt) {
		s.FirstAt = a.FirstAt
	}
	if a.LastAt != nil && (s.LastAt == nil || *a.LastAt > *s.LastAt) {
		s.LastAt = a.LastAt
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
			MergeUsageTotals(&execution.Totals, a.Totals)
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
			MergeUsageTotals(&execution.Measurements[measurement].Totals, a.Totals)
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
	g.addAggregate(UsageAggregate{Record: r, Totals: UsageRecordTotals(r), FirstAt: r.OccurredAt, LastAt: r.OccurredAt})
}

func (g *usageGrouping) addAggregate(a UsageAggregate) {
	r := a.Record
	addSession(g.sessions, a)
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
		MergeUsageTotals(&issue.Totals, a.Totals)
		if a.LastAt != nil && (issue.LastAt == nil || *a.LastAt > *issue.LastAt) {
			issue.LastAt = a.LastAt
		}
		if i == len(path)-1 {
			MergeUsageTotals(&issue.Direct, a.Totals)
			addSession(g.issueSessions[ref.ID], a)
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
