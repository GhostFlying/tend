package traex

import (
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// nativeFailure reads typed error evidence; error prose never authorizes a retry.
func nativeFailure(raw json.RawMessage, willRetry bool) *protocol.AgentFailure {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var e struct {
		Info json.RawMessage `json:"codexErrorInfo"`
	}
	if json.Unmarshal(raw, &e) != nil || len(e.Info) == 0 {
		return &protocol.AgentFailure{Code: "unknown", WillRetry: willRetry}
	}
	var code string
	var status int
	if json.Unmarshal(e.Info, &code) != nil {
		var variants map[string]struct {
			HTTPStatus int `json:"httpStatusCode"`
		}
		if json.Unmarshal(e.Info, &variants) == nil && len(variants) == 1 {
			for k, v := range variants {
				code, status = k, v.HTTPStatus
			}
		}
	}
	if code == "rateLimitExceeded" || ((code == "responseTooManyFailedAttempts" || code == "responseStreamDisconnected" || code == "httpConnectionFailed" || code == "responseStreamConnectionFailed") && status == 429) {
		code = "rate_limit_exceeded"
	}
	if code == "" {
		code = "unknown"
	}
	return &protocol.AgentFailure{Code: code, HTTPStatus: status, WillRetry: willRetry}
}
