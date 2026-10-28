// Package agentconfig validates local agent definitions and public metadata.
// It performs no IO and does not establish runtime availability.
package agentconfig

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type RuntimeKind string
type Purpose string
type SourceKind string

const (
	AppServerWorker RuntimeKind = "appserver-worker"
	OrcaWorker      RuntimeKind = "orca-worker"
	HostedManager   RuntimeKind = "hosted-manager"
	WorkerRun       Purpose     = "worker_run"
	Main            Purpose     = "main"
	IssueManager    Purpose     = "issue_manager"
	Consultation    Purpose     = "consultation"
	LocalFile       SourceKind  = "local_file"
	Inline          SourceKind  = "inline"
)

// Config contains requests, not observed provider settings or availability.
type Config struct {
	FormatVersion uint64            `json:"format_version"`
	Runtimes      []RuntimeBinding  `json:"runtimes"`
	Agents        []AgentDefinition `json:"agents"`
}

type RuntimeBinding struct {
	ID               string      `json:"runtime_id"`
	Revision         uint64      `json:"revision"`
	Kind             RuntimeKind `json:"kind"`
	CLI              string      `json:"cli,omitempty"`
	ExecutableRef    string      `json:"executable_ref"`
	DeclaredPurposes []Purpose   `json:"declared_purposes"`
	Enabled          bool        `json:"enabled"`
}

type AgentDefinition struct {
	LocalID     string    `json:"local_id"`
	Revision    uint64    `json:"revision"`
	DisplayName string    `json:"display_name"`
	RuntimeID   string    `json:"runtime_id"`
	Categories  []string  `json:"categories"`
	Purposes    []Purpose `json:"purposes"`
	Model       string    `json:"model"`
	Effort      string    `json:"effort,omitempty"`
	Skills      []string  `json:"skills,omitempty"`
	MCPServers  []string  `json:"mcp_servers,omitempty"`
	Enabled     bool      `json:"enabled"`
}

// SourceReference identifies local provenance. Only Kind is public metadata.
// Reference remains private and is never opened or interpreted by this package.
type SourceReference struct {
	Kind      SourceKind
	Reference string
}

// Identity keeps its two components separate, including across binding changes.
type Identity struct {
	ExecutionEnvironmentID string `json:"execution_environment_id"`
	LocalID                string `json:"local_id"`
}

// ValidationError deliberately omits offending values and native error text.
type ValidationError struct {
	Code string
	Path string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("agent configuration: %s at %s", e.Code, e.Path)
}

func invalid(code, path string) error { return &ValidationError{Code: code, Path: path} }

// Definitions owns a detached configuration copy. No exported data can mutate it.
type Definitions struct {
	environmentID string
	config        Config
	source        SourceReference
}

func (d *Definitions) Source() SourceReference { return d.source }

func (d *Definitions) Identity(localID string) (Identity, bool) {
	for _, a := range d.config.Agents {
		if a.LocalID == localID {
			return Identity{ExecutionEnvironmentID: d.environmentID, LocalID: a.LocalID}, true
		}
	}
	return Identity{}, false
}

// Validate checks typed callers as well as decoded input. It neither resolves
// executable references nor verifies model catalogs, isolation, or availability.
func Validate(environmentID string, cfg Config, source SourceReference) (*Definitions, error) {
	if !validID(environmentID) {
		return nil, invalid("invalid_id", "$environment_id")
	}
	if source.Kind != LocalFile && source.Kind != Inline {
		return nil, invalid("invalid_source", "$source.kind")
	}
	if source.Kind == LocalFile && strings.TrimSpace(source.Reference) == "" {
		return nil, invalid("required", "$source.reference")
	}
	if cfg.FormatVersion != 1 {
		return nil, invalid("unsupported_format", "$.format_version")
	}
	runtimes := make(map[string]RuntimeBinding, len(cfg.Runtimes))
	for i, r := range cfg.Runtimes {
		path := fmt.Sprintf("$.runtimes[%d]", i)
		if !validID(r.ID) {
			return nil, invalid("invalid_id", path+".runtime_id")
		}
		if _, exists := runtimes[r.ID]; exists {
			return nil, invalid("duplicate_id", path+".runtime_id")
		}
		if r.Revision == 0 {
			return nil, invalid("required", path+".revision")
		}
		if strings.TrimSpace(r.ExecutableRef) == "" {
			return nil, invalid("required", path+".executable_ref")
		}
		switch r.Kind {
		case OrcaWorker:
			if r.CLI != "codex" && r.CLI != "traex" {
				return nil, invalid("unsupported_cli", path+".cli")
			}
		case AppServerWorker, HostedManager:
			if r.CLI != "" {
				return nil, invalid("unsupported_cli", path+".cli")
			}
		default:
			return nil, invalid("unsupported_runtime", path+".kind")
		}
		if err := validatePurposes(r.DeclaredPurposes, r.Kind, path+".declared_purposes"); err != nil {
			return nil, err
		}
		runtimes[r.ID] = r
	}
	seen := make(map[string]bool, len(cfg.Agents))
	for i, a := range cfg.Agents {
		path := fmt.Sprintf("$.agents[%d]", i)
		if !validID(a.LocalID) {
			return nil, invalid("invalid_id", path+".local_id")
		}
		if seen[a.LocalID] {
			return nil, invalid("duplicate_id", path+".local_id")
		}
		seen[a.LocalID] = true
		if a.Revision == 0 {
			return nil, invalid("required", path+".revision")
		}
		if !cleanText(a.DisplayName) {
			return nil, invalid("invalid_label", path+".display_name")
		}
		r, found := runtimes[a.RuntimeID]
		if !found {
			return nil, invalid("missing_runtime", path+".runtime_id")
		}
		if len(a.Categories) == 0 {
			return nil, invalid("required", path+".categories")
		}
		categories := make(map[string]bool, len(a.Categories))
		for j, c := range a.Categories {
			p := fmt.Sprintf("%s.categories[%d]", path, j)
			if !validID(c) {
				return nil, invalid("invalid_id", p)
			}
			if categories[c] {
				return nil, invalid("duplicate_value", p)
			}
			categories[c] = true
		}
		if err := validatePurposes(a.Purposes, r.Kind, path+".purposes"); err != nil {
			return nil, err
		}
		for j, p := range a.Purposes {
			if !slices.Contains(r.DeclaredPurposes, p) {
				return nil, invalid("purpose_mismatch", fmt.Sprintf("%s.purposes[%d]", path, j))
			}
		}
		if a.Model == "" && a.Effort != "" && r.Kind == OrcaWorker {
			return nil, invalid("unsupported_effort_only", path+".effort")
		}
		if !cleanText(a.Model) || len(a.Model) > 200 {
			return nil, invalid("model_required", path+".model")
		}
		if !slices.Contains([]string{"", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}, a.Effort) {
			return nil, invalid("unsupported_effort", path+".effort")
		}
		if len(a.Skills) != 0 {
			return nil, invalid("unsupported_option", path+".skills")
		}
		if len(a.MCPServers) != 0 {
			return nil, invalid("unsupported_option", path+".mcp_servers")
		}
	}
	return &Definitions{environmentID: environmentID, config: cloneConfig(cfg), source: source}, nil
}

func validatePurposes(purposes []Purpose, kind RuntimeKind, path string) error {
	if len(purposes) == 0 {
		return invalid("required", path)
	}
	seen := make(map[Purpose]bool, len(purposes))
	for i, p := range purposes {
		field := fmt.Sprintf("%s[%d]", path, i)
		if seen[p] {
			return invalid("duplicate_value", field)
		}
		seen[p] = true
		if kind == HostedManager {
			if p != Main && p != IssueManager && p != Consultation {
				return invalid("unsupported_purpose", field)
			}
		} else if p != WorkerRun {
			return invalid("unsupported_purpose", field)
		}
	}
	return nil
}

func validID(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i == 0 || c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func cleanText(s string) bool {
	return s != "" && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

func cloneConfig(cfg Config) Config {
	cfg.Runtimes = slices.Clone(cfg.Runtimes)
	for i := range cfg.Runtimes {
		cfg.Runtimes[i].DeclaredPurposes = slices.Clone(cfg.Runtimes[i].DeclaredPurposes)
	}
	cfg.Agents = slices.Clone(cfg.Agents)
	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		a.Categories = slices.Clone(a.Categories)
		a.Purposes = slices.Clone(a.Purposes)
		a.Skills = slices.Clone(a.Skills)
		a.MCPServers = slices.Clone(a.MCPServers)
	}
	return cfg
}
