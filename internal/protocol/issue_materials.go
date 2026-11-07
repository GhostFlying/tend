package protocol

import (
	"net/url"
	"slices"
	"strconv"
)

// IssueMaterialsQuery selects a page or one object from the Issue's retained
// context. It never supplies execution authority or a filesystem location.
type IssueMaterialsQuery struct {
	Kind  string `json:"kind,omitempty"`
	ID    string `json:"id,omitempty"`
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type IssueMaterialSource struct {
	Kind            string `json:"kind"`
	IssueID         string `json:"issue_id"`
	MessageID       string `json:"message_id,omitempty"`
	ContentRevision int64  `json:"content_revision,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	ArtifactID      string `json:"artifact_id,omitempty"`
	AcceptanceID    string `json:"acceptance_id,omitempty"`
	Locator         string `json:"locator,omitempty"`
	Status          string `json:"status,omitempty"`
}

// IssueMaterialFile carries original bytes' metadata and their retained sources.
type IssueMaterialFile struct {
	SourcesMore bool `json:"sources_more,omitempty"`
	ArtifactFile
	MediaType string                `json:"media_type,omitempty"`
	Sources   []IssueMaterialSource `json:"sources"`
}

type IssueMaterialAcceptance struct {
	AcceptanceView
	TaskID    string `json:"task_id"`
	RunID     string `json:"run_id"`
	CreatedAt int64  `json:"created_at"`
}

type IssueMaterialFact struct {
	IssueContextFact
	Superseded bool  `json:"superseded"`
	CreatedAt  int64 `json:"created_at"`
}

type IssueMaterials struct {
	LegacyContexts         []WorkItem                   `json:"legacy_contexts,omitempty"`
	Sources                []IssueConversationItem      `json:"sources,omitempty"`
	Facts                  []IssueMaterialFact          `json:"facts,omitempty"`
	Capabilities           *CapabilityPage              `json:"capabilities,omitempty"`
	AgentCapabilities      *AgentCapabilityPage         `json:"agent_capabilities,omitempty"`
	AgentCapabilityHistory *AgentCapabilityRevisionPage `json:"agent_capability_history,omitempty"`
	TaskCapabilities       *TaskCapabilityExplanation   `json:"task_capabilities,omitempty"`
	Kind                   string                       `json:"kind"`
	IssueIDs               []string                     `json:"issue_ids"`
	ObservedAt             int64                        `json:"observed_at"`
	Seq                    int64                        `json:"seq"`
	Issues                 []IssueView                  `json:"issues,omitempty"`
	Messages               []IssueConversationItem      `json:"messages,omitempty"`
	Tasks                  []TaskDetail                 `json:"tasks,omitempty"`
	Artifacts              []ArtifactDetail             `json:"artifacts,omitempty"`
	Acceptances            []IssueMaterialAcceptance    `json:"acceptances,omitempty"`
	Questions              []DecisionView               `json:"questions,omitempty"`
	Files                  []IssueMaterialFile          `json:"files,omitempty"`
	Deliveries             []IssueDeliveryPage          `json:"deliveries,omitempty"`
	Deployment             *ObservedDeployment          `json:"deployment,omitempty"`
	More                   bool                         `json:"more"`
	Next                   string                       `json:"next,omitempty"`
}

type RunContext struct {
	SpecVersion int64          `json:"spec_version"`
	RunID       string         `json:"run_id"`
	TaskID      string         `json:"task_id"`
	Role        RunRole        `json:"role"`
	Materials   IssueMaterials `json:"materials"`
}

func NormalizeIssueMaterialsQuery(q IssueMaterialsQuery) (IssueMaterialsQuery, error) {
	if q.Kind == "" {
		q.Kind = "overview"
	}
	if !slices.Contains([]string{"overview", "sources", "facts", "messages", "tasks", "artifacts", "acceptances", "questions", "files", "deliveries", "deployment", "capabilities", "agent_capabilities", "agent_capability_history", "task_capabilities"}, q.Kind) {
		return q, Errorf(CodeInvalidRequest, "invalid material kind")
	}
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 {
		return q, Errorf(CodeInvalidRequest, "limit must be between 1 and 100")
	}
	return q, nil
}
func ParseIssueMaterialsQuery(values url.Values) (IssueMaterialsQuery, error) {
	var q IssueMaterialsQuery
	for key, v := range values {
		if !slices.Contains([]string{"kind", "id", "after", "limit"}, key) || len(v) != 1 {
			return q, Errorf(CodeInvalidRequest, "context accepts only kind, id, after and limit")
		}
	}
	q.Kind, q.ID, q.After = values.Get("kind"), values.Get("id"), values.Get("after")
	if raw, ok := values["limit"]; ok {
		value, err := strconv.Atoi(raw[0])
		if err != nil || value < 1 || value > 100 {
			return q, Errorf(CodeInvalidRequest, "limit must be between 1 and 100")
		}
		q.Limit = value
	}
	return NormalizeIssueMaterialsQuery(q)
}
