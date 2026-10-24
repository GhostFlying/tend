package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// notFound maps a missing object to not_found and passes other errors,
// including nil, through.
func notFound(err error, format string, args ...any) error {
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Errorf(protocol.CodeNotFound, format, args...)
	}
	return err
}

type listArgs struct {
	IncludeFinished bool `json:"include_finished"`
	// Project defaults to the session's project; "*" is every project.
	Project *string `json:"project"`
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
	var out []protocol.TaskSummary
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		project := ""
		switch {
		case a.Project != nil && *a.Project != "*":
			project = *a.Project
		case a.Project == nil:
			var err error
			if project, err = app.SessionScope(ctx, tx.Q(), c.Principal); err != nil {
				return err
			}
		}
		var err error
		out, err = views.Tasks(ctx, tx.Q(), a.IncludeFinished, project)
		return err
	})
	return map[string]any{"tasks": out}, err
}

func (s *Service) tasksRead(ctx context.Context, c Call) (any, error) {
	var a taskArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var d protocol.TaskDetail
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		d, err = views.Task(ctx, tx.Q(), a.TaskID)
		return notFound(err, "task %s does not exist", a.TaskID)
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) artifactsRead(ctx context.Context, c Call) (any, error) {
	var a artifactArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	var out protocol.ArtifactDetail
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		out, err = views.Artifact(ctx, tx.Q(), a.ArtifactID)
		return notFound(err, "artifact %s does not exist", a.ArtifactID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) requestsRead(ctx context.Context, c Call) (any, error) {
	var a requestArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	// Only the caller's own requests: its dedup domain.
	var out protocol.RequestView
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		out, err = views.Request(ctx, tx.Q(), c.Principal.DedupDomain(), a.RequestID)
		return notFound(err, "you made no request %s", a.RequestID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) reposList(ctx context.Context, c Call) (any, error) {
	if err := decode(c.Arguments, &noArgs{}); err != nil {
		return nil, err
	}
	type repoView struct {
		protocol.RegisteredRepoView
		BaseCandidates []string `json:"base_candidates,omitempty"`
	}
	type hostView struct {
		HostID string              `json:"host_id"`
		OS     string              `json:"os"`
		Labels []string            `json:"labels,omitempty"`
		Worker protocol.WorkerKind `json:"worker"`
	}
	var out struct {
		Repos     []repoView                 `json:"repos"`
		Templates map[string]json.RawMessage `json:"templates"`
		Hosts     []hostView                 `json:"hosts"`
		// SingleMount says no host's worker supports a workspace with
		// more than one mount, so tasks_create refuses one (D64).
		SingleMount bool `json:"single_mount,omitempty"`
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
		registered, err := views.RegisteredRepos(ctx, q)
		if err != nil {
			return err
		}
		out.Repos = make([]repoView, 0, len(registered))
		candidates := make(map[string][]string, len(repos))
		for _, r := range repos {
			candidates[r.Repo] = r.BaseCandidates
		}
		for _, r := range registered {
			out.Repos = append(out.Repos, repoView{RegisteredRepoView: r, BaseCandidates: candidates[r.Repo]})
		}
		for _, h := range hosts {
			out.Hosts = append(out.Hosts, hostView{h.HostID, h.OS, h.Labels, h.Worker})
		}
		out.SingleMount, err = app.SingleMountOnly(ctx, q)
		if err != nil {
			return err
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

func (s *Service) tendContext(ctx context.Context, c Call) (any, error) {
	if err := decode(c.Arguments, &noArgs{}); err != nil {
		return nil, err
	}
	return s.snapshots.Current(ctx, c.Principal)
}

type sessionProjectArgs struct {
	Project string `json:"project"`
	Reason  string `json:"reason"`
}

func (s *Service) sessionProject(ctx context.Context, c Call) (any, error) {
	var a sessionProjectArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	target := protocol.Target{Type: "agent_session", ID: app.SessionKey(c.Principal.Agent, c.Principal.Thread)}
	return s.execute(ctx, c, "agent.select_project", target, nil, a.Reason, map[string]any{"project": a.Project}, app.SelectProject{})
}
