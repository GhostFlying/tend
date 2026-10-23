package protocol

// ConversationImageInput uploads an image with the owner message that uses it.
type ConversationImageInput struct {
	Name       string `json:"name"`
	MediaType  string `json:"media_type"`
	DataBase64 string `json:"data_base64"`
}

// ConversationImage describes a retained attachment without embedding its bytes.
type ConversationImage struct {
	ImageID   string `json:"image_id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

// ConversationImageData is the authenticated image response.
type ConversationImageData struct {
	ImageID    string `json:"image_id"`
	Name       string `json:"name"`
	MediaType  string `json:"media_type"`
	Size       int64  `json:"size"`
	DataBase64 string `json:"data_base64"`
}

// ConversationImageResponse returns image bytes through owner authentication.
type ConversationImageResponse struct {
	Image ConversationImageData `json:"image"`
}
