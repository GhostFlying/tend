package protocol

// BindFeishu is an owner-controlled destination, never inferred from a callback.
type BindFeishu struct {
	AppID     string `json:"app_id"`
	TenantKey string `json:"tenant_key"`
	SenderID  string `json:"sender_id"`
	ChatID    string `json:"chat_id"`
}

// FeishuBindingView exposes routing identity without any transport credential.
type FeishuBindingView struct {
	Owner string `json:"owner"`
	BindFeishu
	Revision      int64  `json:"revision"`
	Enabled       bool   `json:"enabled"`
	RuntimeHealth string `json:"runtime_health"`
}

// FeishuInputView exposes durable intake/request state without message bodies.
type FeishuInputView struct {
	Sequence          int64  `json:"sequence"`
	EventID           string `json:"event_id"`
	ExternalMessageID string `json:"external_message_id"`
	RequestID         string `json:"request_id"`
	BindingRevision   int64  `json:"binding_revision"`
	MessageID         string `json:"message_id"`
	Status            string `json:"status"`
}
type FeishuInputPage struct {
	Items []FeishuInputView `json:"items"`
	Next  int64             `json:"next"`
	More  bool              `json:"more"`
}
