package protocol

// ConversationAttachment describes an original file retained independently of messages.
type ConversationAttachment struct {
	FileID    string `json:"file_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// DownloadURLResponse grants a short-lived, file-scoped browser download.
type DownloadURLResponse struct {
	URL string `json:"url"`
}
