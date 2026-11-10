package protocol

const (
	OpMigrateOriginals = "storage.originals_migrate"
	OpImportOriginals  = "storage.originals_import"
)

// OriginalMigration uses a bounded batch; the Envelope carries the original
// request identity and migration control revision for an identical retry.
type OriginalMigration struct {
	Limit  int  `json:"limit"`
	Rescan bool `json:"rescan,omitempty"`
}

// OriginalImport indexes a previously uploaded complete JSONL original in place.
// After is a byte cursor at a record boundary, not a local filesystem path.
type OriginalImport struct {
	FileID    string `json:"file_id"`
	SourceKey string `json:"source_key"`
	After     int64  `json:"after,omitempty"`
	Limit     int    `json:"limit"`
}
type OriginalStorageStatus struct {
	Revision            int64 `json:"revision"`
	MigratedRows        int64 `json:"migrated_rows"`
	MigratedBytes       int64 `json:"migrated_bytes"`
	Bodies              int64 `json:"bodies"`
	ReferencedBytes     int64 `json:"referenced_bytes"`
	OriginalFiles       int64 `json:"original_files"`
	OriginalPrefixBytes int64 `json:"original_prefix_bytes"`
	// PendingRows is -1 in a mutation receipt; status reads count pending rows
	// outside the writer. A receipt never scans every historical body.
	PendingRows   int64 `json:"pending_rows"`
	UnmatchedRows int64 `json:"unmatched_rows"`
}
type OriginalMigrationResult struct {
	Status        OriginalStorageStatus `json:"status"`
	MigratedRows  int64                 `json:"migrated_rows"`
	MigratedBytes int64                 `json:"migrated_bytes"`
	More          bool                  `json:"more"`
}
type OriginalImportResult struct {
	Status         OriginalStorageStatus `json:"status"`
	FileID         string                `json:"file_id"`
	SourceKey      string                `json:"source_key"`
	IndexedRecords int                   `json:"indexed_records"`
	IndexedBodies  int                   `json:"indexed_bodies"`
	Next           int64                 `json:"next"`
	More           bool                  `json:"more"`
}
type OriginalVerification struct {
	Checked int    `json:"checked"`
	Bytes   int64  `json:"bytes"`
	Next    string `json:"next,omitempty"`
	More    bool   `json:"more"`
}

// Runner archives provider-owned bytes before uploading related facts. Offsets
// belong to one immutable file generation; paths never cross this boundary.
const OpRunnerRolloutArchive = "runner.rollout_archive"
const MaxNativeRolloutChunkBytes = 256 << 10

type NativeRolloutChunk struct {
	RunID    string `json:"run_id"`
	SourceID string `json:"source_id"`
	ThreadID string `json:"thread_id"`
	Offset   int64  `json:"offset"`
	Data     []byte `json:"data"`
	SHA256   string `json:"sha256"`
}
type NativeRolloutArchiveReceipt struct {
	SourceID      string `json:"source_id"`
	Offset        int64  `json:"offset"`
	NextOffset    int64  `json:"next_offset"`
	SHA256        string `json:"sha256"`
	IndexedOffset int64  `json:"indexed_offset"`
}
