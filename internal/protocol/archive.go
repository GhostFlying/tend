package protocol

import "time"

// ArchiveState is the result of opening an explicitly configured sealed archive.
type ArchiveState string

const (
	ArchiveReady       ArchiveState = "ready"
	ArchiveUnsupported ArchiveState = "unsupported"
	ArchiveUnavailable ArchiveState = "unavailable"
)

// ArchiveMetadata contains only facts actually present in the sealed manifest.
type ArchiveMetadata struct {
	FormatVersion int       `json:"format_version"`
	DeploymentID  string    `json:"deployment_id"`
	Schema        int       `json:"schema"`
	CreatedAt     time.Time `json:"created_at"`
	SourceBuild   string    `json:"source_build,omitempty"`
	RecordKinds   []string  `json:"record_kinds"`
	OriginalCount int       `json:"original_count"`
	ResourceCount int       `json:"resource_count"`
}
type ArchiveSummary struct {
	ArchiveID string           `json:"archive_id"`
	State     ArchiveState     `json:"state"`
	Reason    string           `json:"reason,omitempty"`
	Metadata  *ArchiveMetadata `json:"metadata,omitempty"`
}
type ArchiveCatalog struct {
	Items []ArchiveSummary `json:"items"`
}

// ArchiveRecord preserves historical column values instead of current task DTOs.
type ArchiveRecord struct {
	ArchiveID string         `json:"archive_id"`
	Schema    int            `json:"schema"`
	Kind      string         `json:"kind"`
	ID        string         `json:"id"`
	Data      map[string]any `json:"data"`
}
type ArchiveRecordsPage struct {
	ArchiveID string           `json:"archive_id"`
	Schema    int              `json:"schema"`
	Kind      string           `json:"kind"`
	Items     []map[string]any `json:"items"`
	NextAfter string           `json:"next_after,omitempty"`
}
type ArchiveOriginal struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}
type ArchiveOriginalPage struct {
	ArchiveID string            `json:"archive_id"`
	Items     []ArchiveOriginal `json:"items"`
	NextAfter string            `json:"next_after,omitempty"`
}
type ArchiveResourceFile struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type ArchiveResource struct {
	ID             string                `json:"id"`
	Kind           string                `json:"kind"`
	HostID         string                `json:"host_id,omitempty"`
	RunID          string                `json:"run_id,omitempty"`
	ThreadID       string                `json:"thread_id,omitempty"`
	ParentThreadID string                `json:"parent_thread_id,omitempty"`
	Scope          string                `json:"scope"`
	Status         string                `json:"status"`
	Reason         string                `json:"reason,omitempty"`
	JournalSchema  int                   `json:"journal_schema,omitempty"`
	Files          []ArchiveResourceFile `json:"files"`
}
type ArchiveResourcePage struct {
	ArchiveID string            `json:"archive_id"`
	Items     []ArchiveResource `json:"items"`
	NextAfter string            `json:"next_after,omitempty"`
}
