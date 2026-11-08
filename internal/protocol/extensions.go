package protocol

// CapabilityManagedExtensions negotiates portable, category-scoped Codex content.
const CapabilityManagedExtensions = "runner-managed-extensions-v1"

const ExtensionCompositionVersion = 1

type ExtensionSource struct {
	Type      string `json:"type"`
	URL       string `json:"url,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Package   string `json:"package,omitempty"`
	Version   string `json:"version,omitempty"`
	Subdir    string `json:"subdir,omitempty"`
	Integrity string `json:"integrity,omitempty"`
}

// ExtensionImport is a client-local request. Path is never published as provenance.
type ExtensionImport struct {
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Version string          `json:"version"`
	Path    string          `json:"path,omitempty"`
	Source  ExtensionSource `json:"source"`
}

type ExtensionPackage struct {
	Resource ExtensionResource `json:"resource"`
	Content  []byte            `json:"content"`
}

type ExtensionStageRequest struct {
	RequestID               string           `json:"request_id"`
	ExpectedControlRevision int64            `json:"expected_control_revision"`
	Package                 ExtensionPackage `json:"package"`
}

type ExtensionState struct {
	Revision  int64                `json:"revision"`
	Catalog   ExtensionCatalog     `json:"catalog"`
	Published *ExtensionReleaseRef `json:"published,omitempty"`
}

type ExtensionPlan struct {
	Draft     ExtensionCatalog    `json:"draft"`
	Published *ExtensionRelease   `json:"published,omitempty"`
	Candidate ExtensionReleaseRef `json:"candidate"`
	Changed   bool                `json:"changed"`
	Affected  []AgentIdentity     `json:"affected"`
}

// ExtensionMCP contains portable connection definitions, never credential values.
type ExtensionMCP struct {
	Transport         string            `json:"transport"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	URL               string            `json:"url,omitempty"`
	EnvVars           []string          `json:"env_vars,omitempty"`
	EnvHTTPHeaders    map[string]string `json:"env_http_headers,omitempty"`
	BearerTokenEnvVar string            `json:"bearer_token_env_var,omitempty"`
	StartupTimeoutSec int               `json:"startup_timeout_sec,omitempty"`
	ToolTimeoutSec    int               `json:"tool_timeout_sec,omitempty"`
	EnabledTools      []string          `json:"enabled_tools,omitempty"`
	DisabledTools     []string          `json:"disabled_tools,omitempty"`
	Required          bool              `json:"required,omitempty"`
}

type ExtensionResource struct {
	Kind          string          `json:"kind"`
	ID            string          `json:"id"`
	Version       string          `json:"version"`
	ContentSHA256 string          `json:"content_sha256,omitempty"`
	Size          int64           `json:"size,omitempty"`
	Source        ExtensionSource `json:"source"`
	MCP           *ExtensionMCP   `json:"mcp,omitempty"`
}

// ExtensionChoice is a whole-resource override. Nil enabled means enabled;
// an omitted choice inherits, and explicit false suppresses an inherited item.
type ExtensionChoice struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

type ExtensionBinding struct {
	CategoryID    string            `json:"category_id,omitempty"`
	Agent         AgentIdentity     `json:"agent,omitzero"`
	Revision      int64             `json:"revision"`
	Choices       []ExtensionChoice `json:"choices"`
	AgentsMD      string            `json:"agents_md"`
	BundledSkills *bool             `json:"bundled_skills,omitempty"`
}

type ExtensionCatalog struct {
	Revision   int64               `json:"revision"`
	Resources  []ExtensionResource `json:"resources"`
	Categories []ExtensionBinding  `json:"categories"`
	Agents     []ExtensionBinding  `json:"agents"`
}

type ExtensionRelease struct {
	ID                 string           `json:"id"`
	CompositionVersion int              `json:"composition_version"`
	Catalog            ExtensionCatalog `json:"catalog"`
}

type ResolvedExtension struct {
	Resource ExtensionResource `json:"resource"`
	Origin   string            `json:"origin"`
}

// ExtensionExecutionPin is comparable and independent of native thread identity.
type ExtensionExecutionPin struct {
	ReleaseID        string        `json:"release_id"`
	CategoryID       string        `json:"category_id"`
	CategoryRevision int64         `json:"category_revision"`
	AgentRevision    int64         `json:"agent_revision"`
	Agent            AgentIdentity `json:"agent"`
	EffectiveSHA256  string        `json:"effective_sha256"`
}

type ExtensionCombination struct {
	Pin           ExtensionExecutionPin `json:"pin"`
	Resources     []ResolvedExtension   `json:"resources"`
	Disabled      []ExtensionChoice     `json:"disabled"`
	AgentsMD      string                `json:"agents_md"`
	BundledSkills bool                  `json:"bundled_skills"`
}

type ExtensionReleaseRef struct {
	ID             string `json:"id"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ManifestSize   int64  `json:"manifest_size"`
}

type ExtensionTarget struct {
	Release         ExtensionReleaseRef `json:"release"`
	ManifestPath    string              `json:"manifest_path"`
	ContentBasePath string              `json:"content_base_path"`
}

type ExtensionMCPStatus struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	Source     string `json:"source,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
}

type ExtensionPrepared struct {
	RuntimeID  string                `json:"runtime_id"`
	Pin        ExtensionExecutionPin `json:"pin"`
	Ready      bool                  `json:"ready"`
	Reason     string                `json:"reason,omitempty"`
	Generation string                `json:"generation,omitempty"`
	MCP        []ExtensionMCPStatus  `json:"mcp,omitempty"`
}

type ExtensionAdoption struct {
	MCP            []ExtensionMCPStatus  `json:"mcp,omitempty"`
	Execution      string                `json:"execution"`
	Pin            ExtensionExecutionPin `json:"pin"`
	State          string                `json:"state"`
	PendingRelease string                `json:"pending_release,omitempty"`
	PendingReason  string                `json:"pending_reason,omitempty"`
}

type ExtensionHostStatus struct {
	SkillExclusions   *ExtensionSkillExclusionStatus `json:"skill_exclusions,omitempty"`
	Runtimes          []string                       `json:"runtimes"`
	Enabled           bool                           `json:"enabled"`
	DesiredRelease    string                         `json:"desired_release,omitempty"`
	DownloadedRelease string                         `json:"downloaded_release,omitempty"`
	Prepared          []ExtensionPrepared            `json:"prepared"`
	Adoptions         []ExtensionAdoption            `json:"adoptions"`
	LastError         string                         `json:"last_error,omitempty"`
}

type ExtensionHostView struct {
	State  string               `json:"state"`
	HostID string               `json:"host_id"`
	Online bool                 `json:"online"`
	Status *ExtensionHostStatus `json:"status,omitempty"`
}

type ExtensionSkillExclusionStatus struct {
	ExcludedPaths int    `json:"excluded_paths"`
	LastAttempt   string `json:"last_attempt,omitempty"`
	RefreshedAt   string `json:"refreshed_at,omitempty"`
	NextRefreshAt string `json:"next_refresh_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

type ExtensionStatus struct {
	Next      string               `json:"next,omitempty"`
	More      bool                 `json:"more"`
	Revision  int64                `json:"revision"`
	Published *ExtensionReleaseRef `json:"published,omitempty"`
	Hosts     []ExtensionHostView  `json:"hosts"`
}

type ExtensionEditPayload struct {
	Resource *ExtensionResource `json:"resource,omitempty"`
	Binding  *ExtensionBinding  `json:"binding,omitempty"`
	Kind     string             `json:"kind,omitempty"`
	ID       string             `json:"id,omitempty"`
	Version  string             `json:"version,omitempty"`
}

type ExtensionPublishPayload struct {
	Release  ExtensionRelease    `json:"release"`
	Manifest ExtensionReleaseRef `json:"manifest"`
}

type ExtensionResourcePage struct {
	Items []ExtensionResource `json:"items"`
	Next  string              `json:"next,omitempty"`
	More  bool                `json:"more"`
}

type ExtensionBindingPage struct {
	Items []ExtensionBinding `json:"items"`
	Next  string             `json:"next,omitempty"`
	More  bool               `json:"more"`
}

type ExtensionReleasePage struct {
	Items []ExtensionReleaseRef `json:"items"`
	Next  string                `json:"next,omitempty"`
	More  bool                  `json:"more"`
}
