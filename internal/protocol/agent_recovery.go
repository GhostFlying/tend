package protocol

// AgentFailure retains native classification without parsing user-facing prose.
type AgentFailure struct {
	Code       string `json:"code"`
	HTTPStatus int    `json:"http_status,omitempty"`
	WillRetry  bool   `json:"will_retry,omitempty"`
}

// AgentRecoveryView describes the durable recovery of one hosted message.
type AgentRecoveryView struct {
	Status        string `json:"status"`
	Attempts      int64  `json:"attempts"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty"`
	Deadline      int64  `json:"deadline"`
	Reason        string `json:"reason,omitempty"`
}
