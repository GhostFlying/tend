package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Selection is explicit. Empty selections do not mean restore everything.
type Selection struct {
	Repositories []string `json:"repositories,omitempty"`
	Projects     []string `json:"projects,omitempty"`
	Templates    []string `json:"templates,omitempty"`
	Categories   []string `json:"categories,omitempty"`
	Hosts        []string `json:"hosts,omitempty"`
	MainSettings bool     `json:"main_settings,omitempty"`
}

type RepositorySetting struct {
	Repo           string   `json:"repo"`
	RemoteURL      string   `json:"remote_url"`
	BaseCandidates []string `json:"base_candidates"`
	Retired        bool     `json:"retired,omitempty"`
}
type ProjectSetting struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Weight   int64    `json:"weight"`
	Repos    []string `json:"repos"`
	Template string   `json:"template,omitempty"`
}
type TemplateSetting struct {
	Name      string          `json:"name"`
	Workspace json.RawMessage `json:"workspace"`
}
type CategorySetting struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
}
type HostSetting struct {
	HostID string   `json:"host_id"`
	OS     string   `json:"os"`
	Arch   string   `json:"arch"`
	Labels []string `json:"labels"`
	Repos  []string `json:"repos"`
	Slots  int64    `json:"slots"`
	Worker string   `json:"worker"`
}
type ModelSetting struct {
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
}

// StaticSeed deliberately cannot represent business history, credentials,
// runtime observations, or named Agent definitions. Named definitions remain
// authoritative in the local runtime configuration, including future hosted ones.
type StaticSeed struct {
	FormatVersion      int                 `json:"format_version"`
	SourceDeploymentID string              `json:"source_deployment_id"`
	Repositories       []RepositorySetting `json:"repositories"`
	Projects           []ProjectSetting    `json:"projects"`
	Templates          []TemplateSetting   `json:"templates"`
	Categories         []CategorySetting   `json:"categories"`
	Hosts              []HostSetting       `json:"hosts"`
	MainSettings       *ModelSetting       `json:"main_settings,omitempty"`
}

func (r *Reader) ExportSeed(ctx context.Context, selection Selection) (StaticSeed, error) {
	seed := StaticSeed{FormatVersion: 1, SourceDeploymentID: r.manifest.DeploymentID, Repositories: []RepositorySetting{}, Projects: []ProjectSetting{}, Templates: []TemplateSetting{}, Categories: []CategorySetting{}, Hosts: []HostSetting{}}
	unique := func(ids []string) error {
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" || seen[id] {
				return errors.New("archive: duplicate/empty static selection")
			}
			seen[id] = true
		}
		return nil
	}
	for _, ids := range [][]string{selection.Repositories, selection.Projects, selection.Templates, selection.Categories, selection.Hosts} {
		if err := unique(ids); err != nil {
			return seed, err
		}
	}
	for _, id := range selection.Repositories {
		var value RepositorySetting
		var candidates string
		err := r.db.QueryRowContext(ctx, `SELECT repo,remote_url,base_candidates_json,retired FROM repos WHERE repo=?`, id).Scan(&value.Repo, &value.RemoteURL, &candidates, &value.Retired)
		if err != nil {
			return seed, err
		}
		if err = json.Unmarshal([]byte(candidates), &value.BaseCandidates); err != nil {
			return seed, err
		}
		// Git remotes are exact addresses, not all URLs: SCP syntax and local
		// paths are valid. SSH usernames are transport selectors, not passwords.
		if strings.Contains(value.RemoteURL, "://") {
			parsed, err := url.Parse(value.RemoteURL)
			if err != nil {
				return seed, fmt.Errorf("archive: repository %s has invalid URL syntax", id)
			}
			if parsed.User != nil {
				if strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https") {
					return seed, fmt.Errorf("archive: repository %s has HTTP authentication; provide a credential-free local remote instead", id)
				}
				if _, embedded := parsed.User.Password(); embedded {
					return seed, fmt.Errorf("archive: repository %s has an embedded password; provide a fresh local binding instead", id)
				}
			}
		}
		seed.Repositories = append(seed.Repositories, value)
	}
	for _, id := range selection.Projects {
		var value ProjectSetting
		var repos string
		var template sql.NullString
		err := r.db.QueryRowContext(ctx, `SELECT project,title,weight,repos_json,template FROM projects WHERE project=? AND retired=0`, id).Scan(&value.Name, &value.Title, &value.Weight, &repos, &template)
		if err != nil {
			return seed, err
		}
		if err = json.Unmarshal([]byte(repos), &value.Repos); err != nil {
			return seed, err
		}
		value.Template = template.String
		seed.Projects = append(seed.Projects, value)
	}
	for _, id := range selection.Templates {
		var value TemplateSetting
		var workspace string
		err := r.db.QueryRowContext(ctx, `SELECT name,workspace_json FROM workspace_templates WHERE name=?`, id).Scan(&value.Name, &workspace)
		if err != nil {
			return seed, err
		}
		value.Workspace = json.RawMessage(workspace)
		seed.Templates = append(seed.Templates, value)
	}
	if len(selection.Categories) > 0 && r.manifest.Schema < 55 {
		return seed, errors.New("archive: this historical schema has no categories")
	}
	for _, id := range selection.Categories {
		var value CategorySetting
		err := r.db.QueryRowContext(ctx, `SELECT category_id,title,description,state FROM agent_category_versions WHERE category_id=? ORDER BY revision DESC LIMIT 1`, id).Scan(&value.ID, &value.Title, &value.Description, &value.State)
		if err != nil {
			return seed, err
		}
		seed.Categories = append(seed.Categories, value)
	}
	for _, id := range selection.Hosts {
		var value HostSetting
		var labels, repos string
		err := r.db.QueryRowContext(ctx, `SELECT host_id,os,arch,labels_json,repos_json,slots,worker FROM hosts WHERE host_id=?`, id).Scan(&value.HostID, &value.OS, &value.Arch, &labels, &repos, &value.Slots, &value.Worker)
		if err != nil {
			return seed, err
		}
		if err = json.Unmarshal([]byte(labels), &value.Labels); err != nil {
			return seed, err
		}
		if err = json.Unmarshal([]byte(repos), &value.Repos); err != nil {
			return seed, err
		}
		seed.Hosts = append(seed.Hosts, value)
	}
	if selection.MainSettings {
		value := &ModelSetting{}
		err := r.db.QueryRowContext(ctx, `SELECT model,effort FROM conversation_settings WHERE singleton=1`).Scan(&value.Model, &value.Effort)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return seed, err
		}
		if err == nil {
			seed.MainSettings = value
		}
	}
	// A selected project/host cannot implicitly import unselected repositories or
	// templates through reference expansion.
	for _, project := range seed.Projects {
		for _, repo := range project.Repos {
			if !slices.Contains(selection.Repositories, repo) {
				return seed, errors.New("archive: project references an unselected repository")
			}
		}
		if project.Template != "" && !slices.Contains(selection.Templates, project.Template) {
			return seed, errors.New("archive: project references an unselected template")
		}
	}
	for _, host := range seed.Hosts {
		for _, repo := range host.Repos {
			if !slices.Contains(selection.Repositories, repo) {
				return seed, errors.New("archive: host references an unselected repository")
			}
		}
	}
	return seed, nil
}
