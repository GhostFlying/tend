package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// taskSummary is a task as listed.
type taskSummary struct {
	TaskID          string                 `json:"task_id"`
	Title           string                 `json:"title"`
	Lifecycle       protocol.TaskLifecycle `json:"lifecycle"`
	ControlMode     protocol.ControlMode   `json:"control_mode"`
	ControlRevision int64                  `json:"control_revision"`
	SpecVersion     int64                  `json:"spec_version"`
	Priority        int64                  `json:"priority"`
	BlockReason     protocol.BlockReason   `json:"block_reason"`
	Workspace       string                 `json:"workspace"`
	// NextImplementGeneration is what actions_create needs to retry.
	NextImplementGeneration int64 `json:"next_implement_generation"`
}

func summarize(r store.TaskRow) taskSummary {
	return taskSummary{
		TaskID: r.TaskID, Title: r.Title, Lifecycle: r.Lifecycle, ControlMode: r.ControlMode, ControlRevision: r.ControlRevision,
		SpecVersion: r.SpecVersion, Priority: r.Priority, BlockReason: r.BlockReason, Workspace: r.WorkspaceName,
		NextImplementGeneration: r.ImplementGeneration + 1,
	}
}

func notFound(err error, format string, args ...any) error {
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Errorf(protocol.CodeNotFound, format, args...)
	}
	return err
}

type listArgs struct {
	IncludeFinished bool `json:"include_finished"`
}

type taskArgs struct {
	TaskID string `json:"task_id"`
}

type artifactArgs struct {
	ArtifactID string `json:"artifact_id"`
}

type requestArgs struct {
	RequestID string `json:"request_id"`
}

type noArgs struct{}

func (s *Service) tasksList(ctx context.Context, c Call) (any, error) {
	var a listArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var out []taskSummary
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := store.ListTasks(ctx, tx.Q(), 200)
		for _, r := range rows {
			if a.IncludeFinished || !r.Lifecycle.Terminal() {
				out = append(out, summarize(r))
			}
		}
		return err
	})
	return map[string]any{"tasks": out}, err
}

type taskDetail struct {
	taskSummary
	Spec        json.RawMessage  `json:"spec"`
	Actions     []actionView     `json:"actions"`
	Runs        []runView        `json:"runs"`
	Artifacts   []artifactView   `json:"artifacts"`
	Acceptances []acceptanceView `json:"acceptances"`
	Decisions   []decisionView   `json:"decisions"`
}

type actionView struct {
	ActionID    string                `json:"action_id"`
	Kind        protocol.ActionKind   `json:"kind"`
	Status      protocol.ActionStatus `json:"status"`
	Generation  int64                 `json:"generation"`
	SpecVersion int64                 `json:"spec_version"`
	ArtifactID  string                `json:"artifact_id,omitempty"`
}

type runView struct {
	RunID      string               `json:"run_id"`
	Role       protocol.RunRole     `json:"role"`
	Generation int64                `json:"generation"`
	Host       string               `json:"host"`
	Observed   protocol.RunObserved `json:"observed"`
}

type artifactView struct {
	ArtifactID string          `json:"artifact_id"`
	RunID      string          `json:"run_id"`
	Ref        json.RawMessage `json:"ref"`
}

type acceptanceView struct {
	ArtifactID  string           `json:"artifact_id"`
	SpecVersion int64            `json:"spec_version"`
	Verdict     protocol.Verdict `json:"verdict"`
	Issues      json.RawMessage  `json:"issues"`
	Evidence    json.RawMessage  `json:"evidence,omitempty"`
}

type decisionView struct {
	DecisionID string          `json:"decision_id"`
	Question   string          `json:"question"`
	Options    json.RawMessage `json:"options"`
	Version    int64           `json:"version"`
	Status     string          `json:"status"`
	Answer     json.RawMessage `json:"answer,omitempty"`
}

func acceptances(rows []store.AcceptanceRow, evidence bool) []acceptanceView {
	out := make([]acceptanceView, 0, len(rows))
	for _, a := range rows {
		v := acceptanceView{ArtifactID: a.ArtifactID, SpecVersion: a.SpecVersion, Verdict: a.Verdict, Issues: json.RawMessage(a.IssuesJSON)}
		if evidence {
			v.Evidence = json.RawMessage(a.EvidenceJSON)
		}
		out = append(out, v)
	}
	return out
}

func (s *Service) tasksRead(ctx context.Context, c Call) (any, error) {
	var a taskArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var d taskDetail
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		row, err := store.GetTask(ctx, q, a.TaskID)
		if err != nil {
			return notFound(err, "task %s does not exist", a.TaskID)
		}
		d.taskSummary = summarize(row)
		spec, err := store.GetSpec(ctx, q, row.TaskID, row.SpecVersion)
		if err != nil {
			return err
		}
		d.Spec = json.RawMessage(spec)
		actions, err := store.ListActions(ctx, q, row.TaskID)
		if err != nil {
			return err
		}
		for _, x := range actions {
			d.Actions = append(d.Actions, actionView{x.ActionID, x.Kind, x.Status, x.Generation, x.TargetSpecVersion, x.TargetArtifactID})
		}
		runs, err := store.ListRunsByTask(ctx, q, row.TaskID)
		if err != nil {
			return err
		}
		for _, r := range runs {
			d.Runs = append(d.Runs, runView{r.RunID, r.Role, r.Generation, r.HostID, r.ObservedState})
		}
		arts, err := store.ListArtifactsByTask(ctx, q, row.TaskID)
		if err != nil {
			return err
		}
		for _, x := range arts {
			d.Artifacts = append(d.Artifacts, artifactView{x.ArtifactID, x.RunID, json.RawMessage(x.RefJSON)})
		}
		accs, err := store.ListAcceptances(ctx, q, row.TaskID, "")
		if err != nil {
			return err
		}
		d.Acceptances = acceptances(accs, false)
		decs, err := store.ListDecisions(ctx, q, row.TaskID)
		if err != nil {
			return err
		}
		for _, x := range decs {
			d.Decisions = append(d.Decisions, decisionOf(x))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func decisionOf(x store.DecisionRow) decisionView {
	v := decisionView{DecisionID: x.DecisionID, Question: x.Question, Options: json.RawMessage(x.OptionsJSON), Version: x.Version, Status: x.Status}
	if x.AnswerJSON != "" {
		v.Answer = json.RawMessage(x.AnswerJSON)
	}
	return v
}

func (s *Service) artifactsRead(ctx context.Context, c Call) (any, error) {
	var a artifactArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var out map[string]any
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		art, err := store.GetArtifact(ctx, tx.Q(), a.ArtifactID)
		if err != nil {
			return notFound(err, "artifact %s does not exist", a.ArtifactID)
		}
		accs, err := store.ListAcceptances(ctx, tx.Q(), art.TaskID, art.ArtifactID)
		if err != nil {
			return err
		}
		out = map[string]any{
			"artifact":    artifactView{art.ArtifactID, art.RunID, json.RawMessage(art.RefJSON)},
			"task_id":     art.TaskID,
			"acceptances": acceptances(accs, true),
		}
		return nil
	})
	return out, err
}

func (s *Service) requestsRead(ctx context.Context, c Call) (any, error) {
	var a requestArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	// Only the main agent's own requests: its dedup domain.
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: c.Instance, Thread: c.Thread}
	var out map[string]any
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		rec, err := store.GetRequest(ctx, tx.Q(), principal.DedupDomain(), a.RequestID)
		if err != nil {
			return notFound(err, "you made no request %s", a.RequestID)
		}
		out = map[string]any{"request_id": rec.RequestID, "operation": rec.Operation, "status": rec.Status}
		if rec.ResultJSON != "" {
			out["result"] = json.RawMessage(rec.ResultJSON)
		}
		if rec.ErrorCode != "" {
			pe := &protocol.Error{Code: protocol.Code(rec.ErrorCode), Message: rec.ErrorMessage}
			if rec.ErrorDetails != "" {
				pe.Details = json.RawMessage(rec.ErrorDetails)
			}
			out["error"] = pe
		}
		return nil
	})
	return out, err
}

func (s *Service) reposList(ctx context.Context, c Call) (any, error) {
	if err := decode(c.Arguments, &noArgs{}); err != nil {
		return nil, err
	}
	type repoView struct {
		Repo           string   `json:"repo"`
		Retired        bool     `json:"retired,omitempty"`
		BaseCandidates []string `json:"base_candidates,omitempty"`
		Hosts          []string `json:"hosts"`
	}
	type hostView struct {
		HostID string   `json:"host_id"`
		OS     string   `json:"os"`
		Labels []string `json:"labels,omitempty"`
	}
	var out struct {
		Repos     []repoView                 `json:"repos"`
		Templates map[string]json.RawMessage `json:"templates"`
		Hosts     []hostView                 `json:"hosts"`
	}
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		repos, err := store.ListRepos(ctx, q)
		if err != nil {
			return err
		}
		hosts, err := store.ListActiveHosts(ctx, q)
		if err != nil {
			return err
		}
		for _, r := range repos {
			v := repoView{Repo: r.Repo, Retired: r.Retired, BaseCandidates: r.BaseCandidates, Hosts: []string{}}
			for _, h := range hosts {
				for _, name := range h.Repos {
					if name == r.Repo {
						v.Hosts = append(v.Hosts, h.HostID)
					}
				}
			}
			out.Repos = append(out.Repos, v)
		}
		for _, h := range hosts {
			out.Hosts = append(out.Hosts, hostView{h.HostID, h.OS, h.Labels})
		}
		templates, err := store.ListTemplates(ctx, q)
		if err != nil {
			return err
		}
		out.Templates = map[string]json.RawMessage{}
		for name, body := range templates {
			out.Templates[name] = json.RawMessage(body)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("tools: repos_list: %w", err)
	}
	return out, nil
}
