package orca

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

type failedTurn struct {
	Code           string
	TurnID         string
	TraexRateLimit bool
}

var recoveryErrorCode = regexp.MustCompile(`(?:^|[\s:;"{])code:\s*(invalid_encrypted_content|rate_limit_exceeded)(?:[;\s"}]|$)`)

// TraeX currently loses the provider code for this terminal stream error. Match
// its complete diagnostic only inside a bound TraeX/Trae completion record.
const traexTerminalRateLimit = "stream disconnected before completion: We're sorry, your requests have exceeded the rate limit. Please wait and try again later or contact our support team for assistance."

// readFailedTurn reads only the provider's explicitly bound root transcript.
// Assistant prose, child notifications and old turn errors never classify a
// model failure. Raw provider error text can contain credentials and is not
// persisted or returned to callers.
func readFailedTurn(ctx context.Context, session ProviderSession, since int64) (failedTurn, error) {
	f, err := os.Open(session.TranscriptPath)
	if err != nil {
		return failedTurn{}, errors.New("root transcript cannot be read")
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	identity := ""
	traexProvider := false
	latestTurn := ""
	var result failedTurn
	pending := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return failedTurn{}, err
		}
		var row struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Payload   struct {
				Originator    string `json:"originator"`
				ModelProvider string `json:"model_provider"`
				Type          string `json:"type"`
				ID            string `json:"id"`
				TurnID        string `json:"turn_id"`
				CallID        string `json:"call_id"`
				Error         *struct {
					Code           string          `json:"code"`
					Message        string          `json:"message"`
					CodexErrorInfo json.RawMessage `json:"codex_error_info"`
				} `json:"error"`
			} `json:"payload"`
		}
		if err := decoder.Decode(&row); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return failedTurn{}, errors.New("root transcript contains an incomplete record")
		}
		if row.Type == "session_meta" {
			identity = row.Payload.ID
			traexProvider = row.Payload.Originator == "traex-tui" && row.Payload.ModelProvider == "trae"
			continue
		}
		if row.Timestamp.UnixMilli() < since {
			continue
		}
		p := row.Payload
		if row.Type == "response_item" && p.CallID != "" {
			switch p.Type {
			case "function_call", "custom_tool_call":
				pending[p.CallID] = true
			case "function_call_output", "custom_tool_call_output":
				delete(pending, p.CallID)
			}
		}
		if row.Type != "event_msg" {
			continue
		}
		switch p.Type {
		case "task_started", "turn_started":
			latestTurn = p.TurnID
			result = failedTurn{}
		case "task_complete", "turn_complete":
			if p.TurnID == "" || latestTurn != p.TurnID {
				continue
			}
			result = failedTurn{}
			if p.Error != nil {
				result = failedTurn{Code: classifyRootError(p.Error.Code, p.Error.Message, p.Error.CodexErrorInfo), TurnID: p.TurnID}
				var info string
				if traexProvider && result.Code == "" && p.Error.Code == "" &&
					json.Unmarshal(p.Error.CodexErrorInfo, &info) == nil && info == "other" &&
					p.Error.Message == traexTerminalRateLimit {
					result.Code, result.TraexRateLimit = "rate_limit_exceeded", true
				}
			}
		case "turn_aborted":
			result = failedTurn{}
		}
	}
	if identity != session.ID || result.TurnID == "" {
		return failedTurn{}, errors.New("root transcript does not confirm this session's latest failed turn")
	}
	if len(pending) != 0 {
		return failedTurn{}, errors.New("root transcript has tool calls with unknown outcomes; reconciliation is required")
	}
	return result, nil
}

// temporaryModelError uses Codex's tagged error enum and excludes HTTP client
// errors, which need authentication, specification or policy changes.
func temporaryModelError(raw json.RawMessage) string {
	var code string
	if json.Unmarshal(raw, &code) == nil && code == "rate_limit_exceeded" {
		return code
	}
	var tagged map[string]struct {
		Status *int `json:"http_status_code"`
	}
	if json.Unmarshal(raw, &tagged) != nil {
		return ""
	}
	for _, code := range []string{"http_connection_failed", "response_stream_connection_failed", "response_stream_disconnected"} {
		if info, ok := tagged[code]; ok && (info.Status == nil || *info.Status >= 500) {
			return code
		}
	}
	return ""
}

// classifyRootError consumes only the trusted completion error, never model prose.
func classifyRootError(code, message string, info json.RawMessage) string {
	var typed string
	_ = json.Unmarshal(info, &typed)
	provider := providerBodyCode(message)
	for _, candidate := range []string{code, typed, provider} {
		if candidate == "unauthorized" || candidate == "insufficient_quota" {
			return candidate
		}
	}
	if code == "" {
		code = temporaryModelError(info)
	}
	if provider == "invalid_encrypted_content" || provider == "rate_limit_exceeded" {
		return provider
	}
	if match := recoveryErrorCode.FindStringSubmatch(message); match != nil {
		return match[1]
	}
	return code
}

// Codex formats HTTP errors as "unexpected status <status>: <body>, url: ...".
// Decode the body's first JSON object, allowing the diagnostic suffix. Provider
// codes belong to the root object or its error envelope, not arbitrary nested text.
func providerBodyCode(message string) string {
	start := strings.IndexByte(message, '{')
	if start < 0 {
		return ""
	}
	var body struct {
		Code  string `json:"code"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(strings.NewReader(message[start:])).Decode(&body) != nil {
		return ""
	}
	if body.Error != nil && body.Error.Code != "" {
		return body.Error.Code
	}
	return body.Code
}
