package protocol

import (
	"net/url"
	"strconv"
)

const CapabilityRolloutPreview = "rollout-preview-v1"

// RolloutPreviewQuery addresses native records, never a filesystem path.
type RolloutPreviewQuery struct {
	Field      string `json:"field,omitempty"`
	Reference  int64  `json:"reference,omitempty"`
	Before     int64  `json:"before,omitempty"`
	After      int64  `json:"after,omitempty"`
	Generation string `json:"generation,omitempty"`
	Record     int64  `json:"record,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
}

// RolloutPreviewSource is a locator, not a copy of native content. Paths stay local.
type RolloutPreviewSource struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
}

type RolloutPreviewContent struct {
	References []RolloutPreviewReference `json:"references,omitempty"`
	Text       string                    `json:"text"`
	NextOffset int64                     `json:"next_offset"`
	More       bool                      `json:"more"`
}

// RolloutPreview is an ephemeral read; its state does not change execution state.
type RolloutPreview struct {
	Origin     string                 `json:"origin,omitempty"`
	State      string                 `json:"state"`
	Generation string                 `json:"generation,omitempty"`
	Page       *RunRolloutPage        `json:"page,omitempty"`
	Content    *RolloutPreviewContent `json:"content,omitempty"`
}

// RolloutPreviewWork is scoped by the server to one authenticated runner instance.
type RolloutPreviewWork struct {
	HostID         string                 `json:"host_id"`
	Cancelled      bool                   `json:"cancelled,omitempty"`
	RequireTurn    bool                   `json:"require_turn,omitempty"`
	RequestID      string                 `json:"request_id"`
	DeploymentID   string                 `json:"deployment_id"`
	InstanceID     string                 `json:"instance_id"`
	RunID          string                 `json:"run_id"`
	DeadlineMillis int64                  `json:"deadline_ms"`
	Sources        []RolloutPreviewSource `json:"sources,omitempty"`
	Query          RolloutPreviewQuery    `json:"query"`
}

type RolloutPreviewReply struct {
	RequestID  string         `json:"request_id"`
	InstanceID string         `json:"instance_id"`
	Result     RolloutPreview `json:"result"`
}

// RolloutPreviewReference identifies an explicitly linked sidecar without exposing a locator.
type RolloutPreviewReference struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Missing bool   `json:"missing,omitempty"`
}

// ParseRolloutPreviewQuery shares the bounded cursor contract across owner and CLI reads.
func ParseRolloutPreviewQuery(values url.Values) (RolloutPreviewQuery, error) {
	q := RolloutPreviewQuery{Generation: values.Get("generation"), Field: values.Get("field")}
	invalid := func() (RolloutPreviewQuery, error) { return q, Errorf(CodeInvalidRequest, "invalid preview cursor") }
	for _, field := range []struct {
		name  string
		value *int64
	}{{"before", &q.Before}, {"after", &q.After}, {"record", &q.Record}, {"offset", &q.Offset}, {"reference", &q.Reference}} {
		if text := values.Get(field.name); text != "" {
			value, err := strconv.ParseInt(text, 10, 64)
			if err != nil || value < 0 {
				return invalid()
			}
			*field.value = value
		}
	}
	if values.Get("path") != "" || q.Record > 0 && q.Generation == "" || q.Record == 0 && (q.Offset > 0 || q.Reference > 0 || q.Field != "") || q.Field != "" && q.Field != "raw" && q.Field != "input" && q.Field != "output" && q.Field != "text" || q.Before > 0 && q.After > 0 || len(q.Generation) > 128 {
		return invalid()
	}
	return q, nil
}
